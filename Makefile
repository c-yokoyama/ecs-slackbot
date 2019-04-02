.PHONY: build clean deploy gomodgen

build: gomodgen
	export GO111MODULE=on
	env GOOS=linux go build -ldflags="-s -w" -o bin/handler handler/*

clean:
	rm -rf ./bin ./vendor Gopkg.lock

deploy: clean build
	sls deploy -v --stage ${STAGE}

gomodgen:
	chmod u+x gomod.sh
	./gomod.sh
