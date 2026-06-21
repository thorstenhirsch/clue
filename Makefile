.PHONY: all firmware flash clue clue-test clean

all: firmware clue

firmware:
	cd firmware && tinygo build -target=nicenano -size=short -o ../clue.uf2 .

flash:
	cd firmware && tinygo flash -target=nicenano .

clue:
	go build -o clue ./cmd/clue/

clue-test:
	go build -o clue-test ./cmd/clue-test/

clean:
	rm -f clue.uf2 clue clue-test
