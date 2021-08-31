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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pulumi/pulumi/sdk/v3/go/common/tools"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/cmdutil"

	"github.com/pkg/errors"
	pygen "github.com/pulumi/pulumi/pkg/v3/codegen/python"
	"github.com/pulumi/pulumi/pkg/v3/codegen/schema"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Printf("Usage: pulumi-sdkgen-docker-buildkit <version>\n")
		return
	}

	if err := run(os.Args[1]); err != nil {
		cmdutil.ExitError(err.Error())
	}
}

func run(version string) error {
	spec := schema.PackageSpec{
		Name:              "docker-buildkit",
		Version:           version,
		Description:       "A Pulumi package for building Docker images with Buildkit.",
		License:           "Apache-2.0",
		Repository:        "https://github.com/benesch/pulumi-docker-buildkit",
		PluginDownloadURL: fmt.Sprintf("https://github.com/benesch/pulumi-docker-buildkit/releases/download/v%s/", version),
		Provider:          schema.ResourceSpec{},
		Resources: map[string]schema.ResourceSpec{
			"docker-buildkit:index:Image": {
				ObjectTypeSpec: schema.ObjectTypeSpec{
					Description: "Builds a Docker image using Buildkit and pushes it to a registry.",
					Properties: map[string]schema.PropertySpec{
						"dockerfile": {
							Description: "The path to the Dockerfile to use.",
							TypeSpec:    schema.TypeSpec{Type: "string"},
						},
						"context": {
							Description: "The path to the build context to use.",
							TypeSpec:    schema.TypeSpec{Type: "string"},
						},
						"name": {
							Description: "The name of the image.",
							TypeSpec:    schema.TypeSpec{Type: "string"},
						},
						"platforms": {
							Description: "The platforms to build for.",
							TypeSpec: schema.TypeSpec{
								Type:  "array",
								Items: &schema.TypeSpec{Type: "string"},
							},
						},
						"contextDigest": {
							Description: "The digest of the build context.",
							TypeSpec:    schema.TypeSpec{Type: "string"},
						},
						"repoDigest": {
							Description: "The digest of the image manifest in the registry.",
							TypeSpec:    schema.TypeSpec{Type: "string"},
						},
						"registryServer": {
							Description: "The URL of the registry server hosting the image.",
							TypeSpec:    schema.TypeSpec{Type: "string"},
						},
					},
					Required: []string{
						"dockerfile", "context", "name", "platforms",
						"contextDigest", "repoDigest", "registryServer",
					},
				},
				InputProperties: map[string]schema.PropertySpec{
					"dockerfile": {
						Description: "The path to the Dockerfile to use.",
						TypeSpec:    schema.TypeSpec{Type: "string"},
						Default:     "Dockerfile",
					},
					"context": {
						Description: "The path to the build context to use.",
						TypeSpec:    schema.TypeSpec{Type: "string"},
						Default:     ".",
					},
					"name": {
						Description: "The name of the image.",
						TypeSpec:    schema.TypeSpec{Type: "string"},
					},
					"registry": {
						Description: "The registry to push the image to.",
						TypeSpec: schema.TypeSpec{
							Ref: "#/types/docker-buildkit:index:Registry",
						},
					},
					"platforms": {
						Description: "The platforms to build for.",
						TypeSpec: schema.TypeSpec{
							Type:  "array",
							Items: &schema.TypeSpec{Type: "string"},
						},
					},
				},
				RequiredInputs: []string{"name", "registry"},
			},
		},
		Types: map[string]schema.ComplexTypeSpec{
			"docker-buildkit:index:Registry": {
				ObjectTypeSpec: schema.ObjectTypeSpec{
					Description: "Describes a Docker container registry.",
					Type:        "object",
					Properties: map[string]schema.PropertySpec{
						"server": {
							Description: "The URL of the Docker registry server.",
							TypeSpec:    schema.TypeSpec{Type: "string"},
						},
						"username": {
							Description: "The username to authenticate with.",
							TypeSpec:    schema.TypeSpec{Type: "string"},
						},
						"password": {
							Description: "The password to authenticate with.",
							TypeSpec:    schema.TypeSpec{Type: "string"},
						},
					},
					Required: []string{"server", "username", "password"},
				},
			},
		},
		Language: map[string]json.RawMessage{
			"python": json.RawMessage("{}"),
		},
	}
	ppkg, err := schema.ImportSpec(spec, nil)
	if err != nil {
		return errors.Wrap(err, "reading schema")
	}

	toolDescription := "the Pulumi SDK Generator"
	extraFiles := map[string][]byte{}
	files, err := pygen.GeneratePackage(toolDescription, ppkg, extraFiles)
	if err != nil {
		return fmt.Errorf("generating python package: %v", err)
	}

	for path, contents := range files {
		path = filepath.Join("sdk", "python", path)
		if err := tools.EnsureFileDir(path); err != nil {
			return fmt.Errorf("creating directory: %v", err)
		}
		if err := os.WriteFile(path, contents, 0644); err != nil {
			return fmt.Errorf("writing file: %v", err)
		}
	}

	return nil
}
