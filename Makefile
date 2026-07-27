.PHONY: all firmware flash clue clue-test codex test clean

PROVIDER ?= claude

ifeq ($(PROVIDER),claude)
BUILD_TAGS :=
else ifeq ($(PROVIDER),codex)
BUILD_TAGS := -tags=codex
else
$(error unsupported PROVIDER '$(PROVIDER)'; use claude or codex)
endif

all: firmware clue

firmware:
	cd firmware && tinygo build $(BUILD_TAGS) -target=nicenano -size=short -o ../clue.uf2 .

flash:
	cd firmware && tinygo flash $(BUILD_TAGS) -target=nicenano .

clue:
	go build $(BUILD_TAGS) -o clue ./cmd/clue/

clue-test:
	go build -o clue-test ./cmd/clue-test/

codex:
	$(MAKE) PROVIDER=codex all

test:
	go test ./...
	go test -tags=codex ./...

clean:
	rm -f clue.uf2 clue clue-test
