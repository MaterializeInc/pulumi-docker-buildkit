VERSION ?= $(patsubst v%,%,$(shell git describe))

bin/pulumi-sdkgen-docker-buildkit: cmd/pulumi-sdkgen-docker-buildkit/*.go
	go build -o bin/pulumi-sdkgen-docker-buildkit ./cmd/pulumi-sdkgen-docker-buildkit

sdk: bin/pulumi-sdkgen-docker-buildkit
	rm -rf sdk
	bin/pulumi-sdkgen-docker-buildkit $(VERSION)
	cp README.md sdk/python/
	cp README.md sdk/nodejs/
	cd sdk/python/ && \
		sed -i.bak -e "s/\$${VERSION}/$(VERSION)/g" -e "s/\$${PLUGIN_VERSION}/$(VERSION)/g" setup.py && \
		rm setup.py.bak
	cd sdk/nodejs/ && \
		sed -i.bak -e "s/\$${VERSION}/$(VERSION)/g" package.json && \
		rm package.json.bak

.PHONY: install
install:
	go install ./cmd/pulumi-resource-docker-buildkit
