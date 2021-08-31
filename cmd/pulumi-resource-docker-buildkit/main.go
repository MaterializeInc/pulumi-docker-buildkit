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
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/pkg/fileutils"
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
		return &dockerBuildkitProvider{
			host: host,
		}, nil
	})
	if err != nil {
		cmdutil.ExitError(err.Error())
	}
}

type dockerBuildkitProvider struct {
	host *provider.HostClient
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
	delete(olds, "imageDigest")

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
	context := inputs["context"].StringValue()
	dockerfile := inputs["dockerfile"].StringValue()
	registry := inputs["registry"].ObjectValue()
	username := registry["username"]
	password := registry["password"]

	contextDigest, err := hashContext(context)
	if err != nil {
		return nil, err
	}

	if !username.IsNull() && !password.IsNull() {
		cmd := exec.Command(
			"docker", "login",
			"-u", username.StringValue(), "--password-stdin",
			registry["server"].StringValue(),
		)
		cmd.Stdin = strings.NewReader(password.StringValue())
		if err := runCommand(ctx, k.host, urn, cmd); err != nil {
			return nil, fmt.Errorf("docker login failed: %w", err)
		}
	}

	var platforms []string
	for _, v := range inputs["platforms"].ArrayValue() {
		platforms = append(platforms, v.StringValue())
	}
	cmd := exec.Command(
		"docker", "buildx", "build",
		"--platform", strings.Join(platforms, ","),
		"--cache-from", name,
		"--cache-to", "type=inline",
		"-f", filepath.Join(context, dockerfile),
		"-t", name, "--push",
		context,
	)
	if err := runCommand(ctx, k.host, urn, cmd); err != nil {
		return nil, fmt.Errorf("docker build failed: %w", err)
	}

	cmd = exec.Command("docker", "inspect", name, "-f", "{{index .RepoDigests 0}}")
	repoDigest, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker inspect failed: %s: %s", err, string(repoDigest))
	}

	outputs := map[string]interface{}{
		"dockerfile":     dockerfile,
		"context":        context,
		"name":           name,
		"platforms":      platforms,
		"contextDigest":  contextDigest,
		"imageDigest":    strings.TrimSpace(string(repoDigest)),
		"registryServer": registry["server"].StringValue(),
	}
	return plugin.MarshalProperties(
		resource.NewPropertyMapFromMap(outputs),
		plugin.MarshalOptions{KeepUnknowns: true, SkipNulls: true},
	)
}

func applyDefaults(inputs resource.PropertyMap) {
	if inputs["platforms"].IsNull() {
		inputs["platforms"] = resource.NewArrayProperty(
			[]resource.PropertyValue{resource.NewStringProperty("linux/amd64")},
		)
	}
}

func runCommand(
	ctx context.Context,
	host *provider.HostClient,
	urn resource.URN,
	cmd *exec.Cmd,
) error {
	cmd.Stdout = &logWriter{
		ctx:      ctx,
		host:     host,
		urn:      urn,
		severity: diag.Info,
	}
	cmd.Stderr = &logWriter{
		ctx:      ctx,
		host:     host,
		urn:      urn,
		severity: diag.Info,
	}
	return cmd.Run()
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
	dockerIgnore, err := os.ReadFile(filepath.Join(contextPath, ".dockerignore"))
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("unable to read .dockerignore file: %w", err)
	}
	ignorePatterns, err := dockerignore.ReadAll(bytes.NewReader(dockerIgnore))
	if err != nil {
		return "", fmt.Errorf("unable to parse .dockerignore file: %w", err)
	}
	ignoreMatcher, err := fileutils.NewPatternMatcher(ignorePatterns)
	if err != nil {
		return "", fmt.Errorf("unable to load rules from .dockerignore: %w", err)
	}
	var hashInput []byte
	err = filepath.WalkDir(contextPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path, err = filepath.Rel(contextPath, path)
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		ignore, err := ignoreMatcher.Matches(path)
		if err != nil {
			return fmt.Errorf(".dockerignore rule failed: %w", err)
		}
		if ignore {
			if d.IsDir() {
				return filepath.SkipDir
			} else {
				return nil
			}
		} else if d.IsDir() {
			return nil
		}
		f, err := os.Open(filepath.Join(contextPath, path))
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer f.Close()
		h := sha256.New()
		_, err = io.Copy(h, f)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		hashInput = append(hashInput, path...)
		hashInput = append(hashInput, h.Sum(nil)...)
		hashInput = append(hashInput, byte(0))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("unable to hash build context: %w", err)
	}
	h := sha256.New()
	h.Write(hashInput)
	return hex.EncodeToString(h.Sum(nil)), nil
}
