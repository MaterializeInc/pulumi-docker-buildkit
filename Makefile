VERSION ?= $(patsubst v%,%,$(shell git describe))

bin/pulumi-sdkgen-docker-buildkit: cmd/pulumi-sdkgen-docker-buildkit/*.go
	go build -o bin/pulumi-sdkgen-docker-buildkit ./cmd/pulumi-sdkgen-docker-buildkit

python-sdk: bin/pulumi-sdkgen-docker-buildkit
	rm -rf sdk
	bin/pulumi-sdkgen-docker-buildkit $(VERSION)
	cp README.md sdk/python/
	cd sdk/python/ && \
		sed -i.bak -e "s/\$${VERSION}/$(VERSION)/g" -e "s/\$${PLUGIN_VERSION}/$(VERSION)/g" setup.py && \
		rm setup.py.bak

.PHONY: install
install:
	go install ./cmd/pulumi-resource-docker-buildkit
