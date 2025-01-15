MDs := $(shell find . -name '*.md' -not -path '* *' -not -path '*._*')
HTMLs := $(MDs:.md=.htm)
METAs := $(MDs:.md=.yml)

PWD := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

export ROOT_DIR = $(PWD)
export LUA_FILTER = $(PWD)/desc.lua

%.yml: %.md ./head.tpl
	$(PWD)/meta.sh $< > $@

%.htm: %.md ./head.tpl ./footer.tpl ./article.tpl
	pandoc -s -p --wrap=none \
		--toc \
		--mathml \
		--template article.tpl \
		--highlight-style=pygments \
		--lua-filter $(LUA_FILTER) \
		--metadata=title:$(shell basename $< .md) \
		--metadata=date:$(shell stat -c "%w" $<|cut -d' ' -f1) \
		--from markdown+east_asian_line_breaks \
		$< -o $@

index: $(METAs)
	$(PWD)/index.sh .
	$(PWD)/feed.sh .

all: index $(HTMLs)

clean:
	find . \( -name '*.yml' -o -name '*.htm' \) -type f -exec rm {} \;

.PHONY: index all clean
