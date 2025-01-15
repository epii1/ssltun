#!/usr/bin/env bash

find $1 -type d | while read dir; do

no_child=""

if [[ -f $dir/env ]]; then
	set -o allexport
	source $dir/env
	set +o allexport
fi

# 支持跳过子目录 index.htm
if [[ ! -z "$no_child" ]]; then
	if [[ "$dir" != "." ]]; then
		continue
	fi
fi

tmp=$dir/index.yml
# index.htm 没有对应的 markdown 文件件
# 所以 title 变量为空，pandoc 会输出警告信息
echo "title: no_warn" > $tmp
echo "site_title: $site_title" >> $tmp
echo "articles:" >> $tmp

find $dir -name '*.yml' \
	! -name "draft-*.yml" \
	! -name "wip-*.yml" \
	! -name "index.yml" \
	! -name "feed.yml" \
	-exec cat {} + | grep -viE '"title": "(WIP:.+?)?"' |  sort -r >> $tmp

# 没有 markdown 则不生成 index.htm
if [[ "$(tail -n 1 $tmp)" == "articles:" ]]; then
	rm $tmp
	continue
fi

index=$dir/index.htm

pandoc -s -p -f markdown --wrap=none \
	--template index.tpl \
	--metadata-file=$tmp \
	--lua-filter $LUA_FILTER \
	-o $index /dev/null

[ -s $index ] || rm $index
done
