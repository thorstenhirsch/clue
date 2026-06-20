.PHONY: all firmware flash clue clean

all: firmware clue

firmware:
	cd firmware && tinygo build -target=nicenano -size=short -o ../clue.uf2 .

flash:
	cd firmware && tinygo flash -target=nicenano .

clue:
	go build -o clue ./cmd/clue/

clean:
	rm -f clue.uf2 clue
