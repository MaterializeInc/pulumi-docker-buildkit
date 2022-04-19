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
    args=[docker_buildkit.BuildArgArgs(name="command", value="true")]
)
pulumi.export("image", image.repo_digest)

