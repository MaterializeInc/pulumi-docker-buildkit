VERSION ?= $(patsubst v%,%,$(shell git describe))

bin/pulumi-sdkgen-docker-buildkit: cmd/pulumi-sdkgen-docker-buildkit/*.go
	go build -o bin/pulumi-sdkgen-docker-buildkit ./cmd/pulumi-sdkgen-docker-buildkit

sdk: bin/pulumi-sdkgen-docker-buildkit
	rm -rf sdk
	bin/pulumi-sdkgen-docker-buildkit $(VERSION)
	cd sdk/python/ && \
		sed -i.bak -e "s/0.0.0/$(VERSION)/g" setup.py && \
		rm setup.py.bak
	cd sdk/nodejs/ && \
		npm install && \
		npm run build && \
		awk -f ../../build/munge-package-json.awk -v version=$(VERSION) package.json > bin/package.json
	mv sdk/nodejs sdk/nodejs.tmp
	mv sdk/nodejs.tmp/bin sdk/nodejs
	mv sdk/nodejs.tmp/scripts sdk/nodejs
	rm -r sdk/nodejs.tmp
	cp README.md sdk/python/
	cp README.md sdk/nodejs/

.PHONY: install
install:
	go install ./cmd/pulumi-resource-docker-buildkit
