local description = ""
local refs = {}

function get_description(blocks)
  for _, block in ipairs(blocks) do
    if block.t == 'Para' then
      description = pandoc.utils.stringify(block)
      break
    end
  end
  return nil
end

-- realpath convert path with .. to absolute path
--
-- "/1.md" == realpath("a.md", "1.md")
-- "/1.md" == realpath("/a.md", "1.md")
-- "/1.md" == realpath("/a.md", "./1.md")
-- "/1.md" == realpath("/a/b.md", "../1.md")
-- "/1.md" == realpath("/a/b/c.md", "../../1.md")
-- "/1.md" == realpath("/a/b/c/d.md", "../../../1.md")
function realpath(a, b)
  if a:sub(1,1) ~= '/' then a = '/' .. a end
  c = a:reverse()
  _,j = c:find("/")
  c = c:sub(j+1,-1)
  if b:sub(1,2) == "./" then
    b = b:sub(3)
  end
  while (true) do
    _,i = b:find("../", 1, true)
    _,j = c:find("/")
    if i == nil or j == nil then break end
    b = b:sub(i,-1)
    c = c:sub(j+1,-1)
  end
  if b:sub(1,1) ~= '/' then b = '/' .. b end
  return c:reverse()..b
end

if warn then
  warn '@on'
else
  warn = function (...) io.stderr:write(string.concat({...})) end
end

return {{
  Blocks = get_description,
  Meta = function (meta)
    if meta.desc == nil then
      meta.desc = meta.description
    end
    if meta.desc == nil then
      meta.desc = description
    end
    local path = "/"..PANDOC_STATE.input_files[1]
    meta.path = path:sub(1,-3).."html"

    -- local tags = meta.tags
    -- if tags == nil then tags = {} end
    -- local path_tag = path:gsub("/[^/]+%.md$", "")
    -- if path_tag ~= "" then table.insert(tags, path_tag) end
    -- if next(tags) ~= nil then meta.tags = tags end
    if next(refs) ~= nil then meta.refs = refs end
    return meta
  end,
  Link = function (link)
    local t = link.target
    if t:sub(1,4) ~= "http" and t:sub(-3) == ".md" then
      local p = PANDOC_STATE.input_files[1]
      t = realpath(p, t)
      link.target = t:sub(1,-3).."html"
      table.insert(refs, link.target)
    end
  end,
}}
