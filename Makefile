.PHONY: all firmware flash clue clue-claude clue-codex clue-test claude codex test clean

all: firmware clue-claude clue-codex

firmware:
	cd firmware && tinygo build -target=nicenano -size=short -o ../clue.uf2 .

flash:
	cd firmware && tinygo flash -target=nicenano .

clue: clue-claude clue-codex

clue-claude:
	go build -o clue-claude ./cmd/clue/

clue-codex:
	go build -tags=codex -o clue-codex ./cmd/clue/

clue-test:
	go build -o clue-test ./cmd/clue-test/

claude: clue-claude

codex: clue-codex

test:
	go test ./...
	go test -tags=codex ./...

clean:
	rm -f clue.uf2 clue clue-claude clue-codex clue-test
