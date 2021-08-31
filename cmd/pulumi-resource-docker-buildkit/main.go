// Copyright 2016-2020, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/jsonmessage"
	pbempty "github.com/golang/protobuf/ptypes/empty"
	structpb "github.com/golang/protobuf/ptypes/struct"
	"github.com/moby/buildkit/frontend/dockerfile/dockerignore"
	"github.com/pulumi/pulumi/pkg/v3/resource/provider"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/cmdutil"
	rpc "github.com/pulumi/pulumi/sdk/v3/proto/go"
)

// Injected by linker in release builds.
var version string

func main() {
	err := provider.Main("docker-buildkit", func(host *provider.HostClient) (rpc.ResourceProviderServer, error) {
		client, err := docker.NewClientWithOpts()
		if err != nil {
			return nil, err
		}
		if err = docker.FromEnv(client); err != nil {
			return nil, err
		}
		return &dockerBuildkitProvider{
			host:   host,
			client: client,
		}, nil
	})
	if err != nil {
		cmdutil.ExitError(err.Error())
	}
}

type dockerBuildkitProvider struct {
	host   *provider.HostClient
	client *docker.Client
}

func (k *dockerBuildkitProvider) CheckConfig(ctx context.Context, req *rpc.CheckRequest) (*rpc.CheckResponse, error) {
	return &rpc.CheckResponse{Inputs: req.GetNews()}, nil
}

func (k *dockerBuildkitProvider) DiffConfig(ctx context.Context, req *rpc.DiffRequest) (*rpc.DiffResponse, error) {
	return &rpc.DiffResponse{}, nil
}

func (k *dockerBuildkitProvider) Configure(ctx context.Context, req *rpc.ConfigureRequest) (*rpc.ConfigureResponse, error) {
	return &rpc.ConfigureResponse{}, nil
}

func (k *dockerBuildkitProvider) Invoke(_ context.Context, req *rpc.InvokeRequest) (*rpc.InvokeResponse, error) {
	tok := req.GetTok()
	return nil, fmt.Errorf("Unknown Invoke token '%s'", tok)
}

func (k *dockerBuildkitProvider) StreamInvoke(req *rpc.InvokeRequest, server rpc.ResourceProvider_StreamInvokeServer) error {
	tok := req.GetTok()
	return fmt.Errorf("Unknown StreamInvoke token '%s'", tok)
}

func (k *dockerBuildkitProvider) Check(ctx context.Context, req *rpc.CheckRequest) (*rpc.CheckResponse, error) {
	urn := resource.URN(req.GetUrn())
	ty := urn.Type()
	if ty != "docker-buildkit:index:Image" {
		return nil, fmt.Errorf("Unknown resource type '%s'", ty)
	}
	return &rpc.CheckResponse{Inputs: req.News, Failures: nil}, nil
}

func (k *dockerBuildkitProvider) Diff(ctx context.Context, req *rpc.DiffRequest) (*rpc.DiffResponse, error) {
	urn := resource.URN(req.GetUrn())
	ty := urn.Type()
	if ty != "docker-buildkit:index:Image" {
		return nil, fmt.Errorf("Unknown resource type '%s'", ty)
	}

	olds, err := plugin.UnmarshalProperties(req.GetOlds(), plugin.MarshalOptions{KeepUnknowns: true, SkipNulls: true})
	if err != nil {
		return nil, err
	}
	delete(olds, "repoDigest")

	news, err := plugin.UnmarshalProperties(req.GetNews(), plugin.MarshalOptions{KeepUnknowns: true, SkipNulls: true})
	if err != nil {
		return nil, err
	}
	applyDefaults(news)
	news["registryServer"] = news["registry"].ObjectValue()["server"]
	delete(news, "registry")
	contextDigest, err := hashContext(news["context"].StringValue())
	if err != nil {
		return nil, err
	}
	news["contextDigest"] = resource.NewStringProperty(contextDigest)

	d := olds.Diff(news)
	if d == nil {
		return &rpc.DiffResponse{
			Changes: rpc.DiffResponse_DIFF_NONE,
		}, nil
	}

	diff := map[string]*rpc.PropertyDiff{}
	for key := range d.Adds {
		diff[string(key)] = &rpc.PropertyDiff{Kind: rpc.PropertyDiff_ADD}
	}
	for key := range d.Deletes {
		diff[string(key)] = &rpc.PropertyDiff{Kind: rpc.PropertyDiff_DELETE}
	}
	for key := range d.Updates {
		diff[string(key)] = &rpc.PropertyDiff{Kind: rpc.PropertyDiff_UPDATE}
	}
	return &rpc.DiffResponse{
		Changes:         rpc.DiffResponse_DIFF_SOME,
		DetailedDiff:    diff,
		HasDetailedDiff: true,
	}, nil
}

func (k *dockerBuildkitProvider) Create(ctx context.Context, req *rpc.CreateRequest) (*rpc.CreateResponse, error) {
	urn := resource.URN(req.GetUrn())
	ty := urn.Type()
	if ty != "docker-buildkit:index:Image" {
		return nil, fmt.Errorf("Unknown resource type '%s'", ty)
	}
	outputProperties, err := k.dockerBuild(ctx, urn, req.GetProperties())
	if err != nil {
		return nil, err
	}
	return &rpc.CreateResponse{
		Id:         "ignored",
		Properties: outputProperties,
	}, nil
}

func (k *dockerBuildkitProvider) Read(ctx context.Context, req *rpc.ReadRequest) (*rpc.ReadResponse, error) {
	urn := resource.URN(req.GetUrn())
	ty := urn.Type()
	if ty != "docker-buildkit:index:Image" {
		return nil, fmt.Errorf("Unknown resource type '%s'", ty)
	}
	return &rpc.ReadResponse{
		Id:         req.GetId(),
		Properties: req.GetProperties(),
	}, nil
}

func (k *dockerBuildkitProvider) Update(ctx context.Context, req *rpc.UpdateRequest) (*rpc.UpdateResponse, error) {
	urn := resource.URN(req.GetUrn())
	ty := urn.Type()
	if ty != "docker-buildkit:index:Image" {
		return nil, fmt.Errorf("Unknown resource type '%s'", ty)
	}
	outputProperties, err := k.dockerBuild(ctx, urn, req.GetNews())
	if err != nil {
		return nil, err
	}
	return &rpc.UpdateResponse{
		Properties: outputProperties,
	}, nil
}

func (k *dockerBuildkitProvider) Delete(ctx context.Context, req *rpc.DeleteRequest) (*pbempty.Empty, error) {
	urn := resource.URN(req.GetUrn())
	ty := urn.Type()
	if ty != "docker-buildkit:index:Image" {
		return nil, fmt.Errorf("Unknown resource type '%s'", ty)
	}
	// Not possible to delete Docker images via the registry API.
	return &pbempty.Empty{}, nil
}

func (k *dockerBuildkitProvider) Construct(_ context.Context, _ *rpc.ConstructRequest) (*rpc.ConstructResponse, error) {
	panic("Construct not implemented")
}

func (k *dockerBuildkitProvider) GetPluginInfo(context.Context, *pbempty.Empty) (*rpc.PluginInfo, error) {
	return &rpc.PluginInfo{
		Version: version,
	}, nil
}

func (k *dockerBuildkitProvider) GetSchema(ctx context.Context, req *rpc.GetSchemaRequest) (*rpc.GetSchemaResponse, error) {
	return &rpc.GetSchemaResponse{}, nil
}

func (k *dockerBuildkitProvider) Cancel(context.Context, *pbempty.Empty) (*pbempty.Empty, error) {
	return &pbempty.Empty{}, nil
}

func (k *dockerBuildkitProvider) dockerBuild(
	ctx context.Context,
	urn resource.URN,
	props *structpb.Struct,
) (*structpb.Struct, error) {
	inputs, err := plugin.UnmarshalProperties(props, plugin.MarshalOptions{KeepUnknowns: true, SkipNulls: true})
	if err != nil {
		return nil, err
	}
	applyDefaults(inputs)
	name := inputs["name"].StringValue()
	baseName := strings.Split(name, ":")[0]
	context := inputs["context"].StringValue()
	dockerfile := inputs["dockerfile"].StringValue()
	registry := inputs["registry"].ObjectValue()
	username := registry["username"]
	password := registry["password"]

	contextDigest, err := hashContext(context)
	if err != nil {
		return nil, err
	}

	var authToken string
	if !username.IsNull() && !password.IsNull() {
		auth := types.AuthConfig{
			Username:      username.StringValue(),
			Password:      password.StringValue(),
			ServerAddress: registry["server"].StringValue(),
		}
		_, err := k.client.RegistryLogin(ctx, auth)
		if err != nil {
			return nil, fmt.Errorf("docker login failed: %v", err)
		}
		authConfigBytes, _ := json.Marshal(auth)
		authToken = base64.URLEncoding.EncodeToString(authConfigBytes)
	}

	var platforms []string
	for _, v := range inputs["platforms"].ArrayValue() {
		platforms = append(platforms, v.StringValue())
	}
	buildCtx, err := buildContext(context)
	if err != nil {
		return nil, fmt.Errorf("could not assemble docker build context for %#v: %v", context, err)
	}
	imageBuild := types.ImageBuildOptions{
		Tags:       []string{name},
		Dockerfile: dockerfile,
		Context:    buildCtx,
		Version:    types.BuilderBuildKit,
		Remove:     true,
		CacheFrom:  []string{name},
		Platform:   strings.Join(platforms, ","),
		// TODO: I don't know how to get --cache-to=type=internal set here /:
	}
	res, buildErr := k.client.ImageBuild(ctx, buildCtx, imageBuild)
	if buildErr != nil {
		return nil, fmt.Errorf("image build failed: %v", buildErr)
	}
	defer res.Body.Close()
	if err = streamResponse(ctx, k.host, urn, diag.Info, diag.Error, res.Body); err != nil {
		return nil, fmt.Errorf("could not stream build response: %v", err)
	}

	pushRes, pushErr := k.client.ImagePush(ctx, name, types.ImagePushOptions{
		RegistryAuth: authToken,
		Platform:     strings.Join(platforms, ","),
	})
	if pushErr != nil {
		return nil, fmt.Errorf("image push failed: %v", pushErr)
	}
	if err = streamResponse(ctx, k.host, urn, diag.Info, diag.Error, pushRes); err != nil {
		return nil, fmt.Errorf("could not stream push response: %v", err)
	}
	defer pushRes.Close()

	inspect, _, err := k.client.ImageInspectWithRaw(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("docker inspect failed: %v", err)
	}
	var repoDigest string
	for _, line := range inspect.RepoDigests {
		repo := strings.Split(line, "@")[0]
		if repo == baseName {
			repoDigest = line
			break
		}
	}
	if repoDigest == "" {
		return nil, fmt.Errorf("failed to find repo digest for %#v in docker inspect : %v", name, inspect.RepoDigests)
	}

	outputs := map[string]interface{}{
		"dockerfile":     filepath.Join(context, dockerfile),
		"context":        context,
		"name":           name,
		"platforms":      platforms,
		"contextDigest":  contextDigest,
		"repoDigest":     repoDigest,
		"registryServer": registry["server"].StringValue(),
	}
	return plugin.MarshalProperties(
		resource.NewPropertyMapFromMap(outputs),
		plugin.MarshalOptions{KeepUnknowns: true, SkipNulls: true},
	)
}

func streamResponse(ctx context.Context, host *provider.HostClient, urn resource.URN, okSev diag.Severity, errSev diag.Severity, jsonIn io.Reader) error {
	// TODO: find out how to use DisplayJSONMessagesStream instead of hand-knitting ours.
	dec := json.NewDecoder(jsonIn)
	for {
		var m jsonmessage.JSONMessage
		err := dec.Decode(&m)
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if m.Error != nil {
			host.Log(ctx, errSev, urn, m.ErrorMessage)
		} else {
			host.Log(ctx, okSev, urn, m.ProgressMessage) // TODO: log more progress data
		}
	}
	return nil
}

func applyDefaults(inputs resource.PropertyMap) {
	if inputs["platforms"].IsNull() {
		inputs["platforms"] = resource.NewArrayProperty(
			[]resource.PropertyValue{resource.NewStringProperty("linux/amd64")},
		)
	}
}

type logWriter struct {
	ctx      context.Context
	host     *provider.HostClient
	urn      resource.URN
	severity diag.Severity
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	return len(p), w.host.Log(w.ctx, w.severity, w.urn, string(p))
}

func hashContext(contextPath string) (string, error) {
	context, err := buildContext(contextPath)
	if err != nil {
		return "", fmt.Errorf("unable to hash build context: %w", err)
	}
	defer context.Close()
	h := sha256.New()
	io.Copy(h, context)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func buildContext(contextPath string) (io.ReadCloser, error) {
	dockerIgnore, err := os.ReadFile(filepath.Join(contextPath, ".dockerignore"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("unable to read .dockerignore file: %w", err)
	}
	ignorePatterns, err := dockerignore.ReadAll(bytes.NewReader(dockerIgnore))
	if err != nil {
		return nil, fmt.Errorf("unable to parse .dockerignore file: %w", err)
	}
	return archive.TarWithOptions(contextPath, &archive.TarOptions{ExcludePatterns: ignorePatterns})
}
