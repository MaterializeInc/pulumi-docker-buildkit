import * as aws from "@pulumi/aws";
import * as dockerBuildkit from "@materializeinc/pulumi-docker-buildkit";

const repo = new aws.ecr.Repository("repo");

const registryInfo = repo.registryId.apply(async id => {
    const credentials = await aws.ecr.getCredentials({ registryId: id });
    const authToken = Buffer.from(credentials.authorizationToken, "base64");
    const [username, password] = authToken.toString().split(":");
    return { server: credentials.proxyEndpoint, username: username, password: password };
});

export const image = new dockerBuildkit.Image(
    "image",
    {
        name: repo.repositoryUrl,
        registry: registryInfo,
        args: [{name: "command", value: "true"}],
    },
).repoDigest;
