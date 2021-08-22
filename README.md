# Docker Buildkit Pulumi Provider

A [Pulumi](https://pulumi.com) provider that builds and pushes a Docker image to
a registry using [Buildkit].

## Motivation

Why use this provider over the official [pulumi-docker] provider? This provider
fixes many of the [bugs](https://github.com/pulumi/pulumi-docker/issues/132)
with the official Docker provider:

* `pulumi preview` does not silently block while waiting for the Docker image
  to build.
* Output from `docker build` streams to the terminal during `pulumi up`.
* `docker build` is not invoked if nothing in the build context has changed.
* Changes to the build context cause a diff to appear during `pulumi preview`.

It also provides several new features:

* Support for cross-building images (e.g., building a `linux/arm64` machine on
  a `linux/amd64` host.)
* Automatic inline caching.

There are a few limitations though. The `Image` resource is much less
configurable than the
[`Image`](https://www.pulumi.com/docs/reference/pkg/docker/image/) resource in
the official Docker provider. And there is no support whatsoever for the other
resource types, like `Container` or `Secret`.

## Usage example

To build and push an image to an AWS ECR repository:

```python
import base64

import pulumi
import pulumi_aws as aws
import pulumi_docker_buildkit as docker_buildkit

def get_registry_info(registry_id):
    credentials = aws.ecr.get_credentials(registry_id)
    username, password = base64.b64decode(credentials.authorization_token).decode().split(":")
    return docker_buildkit.RegistryArgs(
        server=credentials.proxy_endpoint,
        username=username,
        password=password,
    )


repo = aws.ecr.Repository("repo")
image = docker_buildkit.Image(
    "image",
    name=repo.repository_url,
    registry=repo.registry_id.apply(get_registry_info),
)
```

**Warning:** Be sure to aggressively exclude files in your `.dockerignore`. The
`Image` resource hashes all files in the build context before determining
whether to invoke `docker build`. This is fast, unless you have tens of
thousands of files in your build context. The `.git` directory and
`node_modules` are the usual culprits.

## Future plans

I plan to make minor bugfixes as necessary for our use of this provider at
[@MaterializeInc](https://github.com/MaterializeInc). I do not currently plan to
bring it to feature parity with the official Docker provider, nor do I have the
time to entertain such contributions. Sorry! I encourage you to either fork this
repository or to use the ideas here to improve the official Pulumi Docker
provider.

[pulumi-docker]: https://github.com/pulumi/pulumi-docker
[Buildkit]: http://github.com/moby/buildkit
