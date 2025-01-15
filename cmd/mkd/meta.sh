#!/usr/bin/env bash

title=$(basename $1 .md)
date=$(stat -c "%w" $1|cut -d' ' -f1)
updated=`date -u +"%Y-%m-%dT%H:%M:%SZ" -r $1`

pandoc -s -p -f markdown --wrap=none \
	--template $ROOT_DIR/meta.tpl \
	--metadata=title:$title \
	--metadata=date:$date \
	--metadata=updated:$updated \
	--lua-filter $ROOT_DIR/meta.lua \
	-o - $1
