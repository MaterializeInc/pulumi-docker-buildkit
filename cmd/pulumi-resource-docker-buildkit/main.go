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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

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
	host      *provider.HostClient
	loginLock sync.Mutex
}

func (k *dockerBuildkitProvider) Call(ctx context.Context, req *rpc.CallRequest) (*rpc.CallResponse, error) {
	return nil, fmt.Errorf("Call is not yet implemented")
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
	contextDigest, err := hashContext(
		news["context"].StringValue(),
		news["dockerfile"].StringValue(),
	)
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
	target := inputs["target"].StringValue()
	registry := inputs["registry"].ObjectValue()
	username := registry["username"]
	password := registry["password"]

	contextDigest, err := hashContext(context, dockerfile)
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
		// On macOS, it seems simultaneous invocations of `docker login` can
		// fail. See #6. Use a lock to prevent multiple `dockerBuild` requests
		// from calling `docker login` simultaneously.
		k.loginLock.Lock()
		err := runCommand(ctx, k.host, urn, cmd)
		k.loginLock.Unlock()
		if err != nil {
			return nil, fmt.Errorf("docker login failed: %w", err)
		}
	}

	var platforms []string
	for _, v := range inputs["platforms"].ArrayValue() {
		platforms = append(platforms, v.StringValue())
	}

	var arguments []string = []string{
		"buildx", "build",
		"--platform", strings.Join(platforms, ","),
		"--cache-from", name,
		"--cache-to", "type=inline",
		"-f", filepath.Join(context, dockerfile),
		"--target", target,
		"-t", name, "--push",
	}

	if !inputs["args"].IsNull() {
		for _, v := range inputs["args"].ArrayValue() {
			arguments = append(arguments, "--build-arg")
			arguments = append(arguments, fmt.Sprintf("%s=%s", v.ObjectValue()["key"].StringValue(), v.ObjectValue()["value"].StringValue()))
		}
	}

	arguments = append(arguments, context)

	cmd := exec.Command(
		"docker", arguments...,
	)
	if err := runCommand(ctx, k.host, urn, cmd); err != nil {
		return nil, fmt.Errorf("docker build failed: %w", err)
	}

	cmd = exec.Command("docker", "inspect", name, "-f", `{{join .RepoDigests "\n"}}`)
	repoDigests, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker inspect failed: %s: %s", err, string(repoDigests))
	}
	var repoDigest string
	for _, line := range strings.Split(string(repoDigests), "\n") {
		repo := strings.Split(line, "@")[0]
		if repo == baseName {
			repoDigest = line
			break
		}
	}
	if repoDigest == "" {
		return nil, fmt.Errorf("failed to find repo digest in docker inspect output: %s", repoDigests)
	}

	outputs := map[string]interface{}{
		"dockerfile":     dockerfile,
		"context":        context,
		"target":         target,
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

type contextHash struct {
	contextPath string
	input       bytes.Buffer
}

func newContextHash(contextPath string) *contextHash {
	return &contextHash{contextPath: contextPath}
}

func (ch *contextHash) hashPath(path string, fileMode fs.FileMode) error {
	f, err := os.Open(filepath.Join(ch.contextPath, path))
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	ch.input.Write([]byte(path))
	ch.input.Write([]byte(fileMode.String()))
	ch.input.Write(h.Sum(nil))
	ch.input.WriteByte(0)
	return nil
}

func (ch *contextHash) hexSum() string {
	h := sha256.New()
	ch.input.WriteTo(h)
	return hex.EncodeToString(h.Sum(nil))
}

func hashContext(contextPath string, dockerfile string) (string, error) {
	dockerIgnorePath := dockerfile + ".dockerignore"
	dockerIgnore, err := os.ReadFile(dockerIgnorePath)
	if err != nil {
		if os.IsNotExist(err) {
			dockerIgnorePath = filepath.Join(contextPath, ".dockerignore")
			dockerIgnore, err = os.ReadFile(dockerIgnorePath)
			if err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("unable to read %s file: %w", dockerIgnorePath, err)
			}
		} else {
			return "", fmt.Errorf("unable to read %s file: %w", dockerIgnorePath, err)
		}
	}
	ignorePatterns, err := dockerignore.ReadAll(bytes.NewReader(dockerIgnore))
	if err != nil {
		return "", fmt.Errorf("unable to parse %s file: %w", dockerIgnorePath, err)
	}
	ignoreMatcher, err := fileutils.NewPatternMatcher(ignorePatterns)
	if err != nil {
		return "", fmt.Errorf("unable to load rules from %s: %w", dockerIgnorePath, err)
	}
	ch := newContextHash(contextPath)
	err = ch.hashPath(dockerfile, 0)
	if err != nil {
		return "", fmt.Errorf("hashing dockerfile %q: %w", dockerfile, err)
	}
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
			return fmt.Errorf("%s rule failed: %w", dockerIgnorePath, err)
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
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("determining mode for %q: %w", path, err)
		}
		err = ch.hashPath(path, info.Mode())
		if err != nil {
			return fmt.Errorf("hashing %q: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("unable to hash build context: %w", err)
	}
	return ch.hexSum(), nil
}
