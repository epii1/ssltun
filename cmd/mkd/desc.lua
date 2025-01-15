local description = ""
local runes = 0

function get_description(blocks)
  for _, block in ipairs(blocks) do
    if block.t == 'Para' then
      description = pandoc.utils.stringify(block)
      break
    end
  end

  for _, block in ipairs(blocks) do
    if block.t == 'Para' then
      local s = pandoc.utils.stringify(block)
      s,_ = s:gsub("[0-9a-zA-Z-]+","a")
      s,_ = s:gsub("[ \t\r\n]+","")

      runes = runes + utf8.len(s)
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
    _,i = b:find("../")
    _,j = c:find("/")
    if i == nil or j == nil then break end
    b = b:sub(i,-1)
    c = c:sub(j+1,-1)
  end
  if b:sub(1,1) ~= '/' then b = '/' .. b end
  return c:reverse()..b
end

-- Report Lua warnings to stderr
if warn then
  warn '@on'
else
  warn = function (...) io.stderr:write(string.concat({...})) end
end

local system = require 'pandoc.system'
local with_temporary_directory = system.with_temporary_directory
local with_working_directory = system.with_working_directory

local function read_file (filepath)
  local fh = io.open(filepath, 'rb')
  local contents = fh:read('a')
  fh:close()
  return contents
end

local function write_file (filepath, content)
  local fh = io.open(filepath, 'wb')
  fh:write(content)
  fh:close()
end

--- GraphViz engine
local graphviz = {
  line_comment_start = '//',
  compile = function (self, code)
    return pandoc.pipe('dot', {"-Tsvg"}, code)
  end,
}

--- XeLaTeX engine
local xelatex = {
  line_comment_start = '%%',
  compile = function (self, code, user_opts)
    return with_temporary_directory('tex', function (tmpdir)
      return with_working_directory(tmpdir, function ()
        code = "\\documentclass[tikz,dvisvgm]{standalone}\n" .. code
        write_file('draw.tex', code)

        local args = {'-halt-on-error', '-no-pdf', 'draw.tex'}
        pandoc.pipe('xelatex', args, '')
        pandoc.pipe('dvisvgm', {'draw.xdv', '-o', '%f'}, '')

        return read_file('draw.svg')
      end)
    end)
  end
}

--- Asymptote diagram engine
local asymptote = {
  line_comment_start = '//',
  compile = function (self, code)
    return with_temporary_directory('asymptote', function(tmpdir)
      return with_working_directory(tmpdir, function ()
        local args = {'-tex', 'xelatex', '-f', 'svg', '-o', 'draw', '-'}
        pandoc.pipe('asy', args, code)
        return read_file('draw.svg')
      end)
    end)
  end,
}

local engines = {
  ['asy.svg'] = asymptote,
  ['dot.svg'] = graphviz,
  ['tex.svg'] = xelatex,
}

local function properties_from_code (code, comment_start)
  local props = {}
  local pattern = comment_start:gsub('%p', '%%%1') .. '|%s+' ..
    '([-_%w]+):%s+([^\n]*)\n'
  for key, value in code:gmatch(pattern) do
    if key == 'fig-cap' then
      props['caption'] = value
    else
      props[key] = value
    end
  end
  return props
end

local function diagram_options (cb, comment_start)
  local attribs = comment_start
    and properties_from_code(cb.text, comment_start)
    or {}
  for key, value in pairs(cb.attributes) do
    attribs[key] = value
  end

  -- Read caption attribute as Markdown
  local caption = attribs.caption
    and pandoc.read(attribs.caption).blocks
    or nil
  local fig_attr = {
    id = cb.identifier ~= '' and cb.identifier or attribs.label,
    name = attribs.name,
  }
  local user_opt = {}

  for k, v in pairs(attribs) do
    local prefix, key = k:match '^(%a+)%-(%a[-%w]*)$'
    if prefix == 'fig' then
      fig_attr[key] = v
    elseif prefix == 'opt' then
      user_opt[key] = v
    end
  end

  return {
    ['alt'] = attribs.alt or
      (caption and pandoc.utils.blocks_to_inlines(caption)) or
      {},
    ['caption'] = caption,
    ['fig-attr'] = fig_attr,
    ['filename'] = attribs.filename,
    ['image-attr'] = {
      height = attribs.height,
      width = attribs.width,
      style = attribs.style,
      loading = 'lazy',
    },
    ['opt'] = user_opt,
  }
end

local draws = 0

-- Executes each document's code block to find matching code blocks:
local function code_to_figure (block)
  -- Check if a converter exists for this block. If not, return the block
  -- unchanged.
  local diagram_type = block.classes[1]
  if not diagram_type then
    return nil
  end

  local engine = engines[diagram_type]
  if not engine then
    local img = block.attributes["img"]
    if not img then
      return nil
    end
    diagram_type = diagram_type.."."..img
    engine = engines[diagram_type]
    if not engine then
      return nil
    end
  end

  -- Unified properties.
  local dgr_opt = diagram_options(block, engine.line_comment_start)
  for optname, value in pairs(engine.opt or {}) do
    dgr_opt.opt[optname] = dgr_opt.opt[optname] or value
  end

  -- No cached image; call the converter
  local ok, imgdata = pcall(engine.compile, engine, block.text, dgr_opt.opt)

  -- Bail if an error occurred; imgdata contains the error message
  -- when that happens.
  if not ok then
    warn(PANDOC_SCRIPT_FILE, ': ', tostring(imgdata))
    return nil
  elseif not imgdata then
    warn(PANDOC_SCRIPT_FILE, ': Diagram engine returned no image data.')
    return nil
  end

  -- Use the a name by hashing the image content.
  draws = draws + 1
  local basename, _ = pandoc.path.split_extension(PANDOC_STATE.input_files[1])
  local fname = basename .. '.' .. draws .. '.svg'

  if not dgr_opt.opt['embed-font'] then
    imgdata = string.gsub(imgdata, "\n<defs>.-</defs>\n", "")
  end

  write_file(fname, imgdata)

  -- Create the image object.
  local image = pandoc.Image(dgr_opt.alt, "/"..fname, "", dgr_opt['image-attr'])
  image.attributes.class = 'code'

  -- Create a figure if the diagram has a caption; otherwise return
  -- just the image.
  return dgr_opt.caption and
    pandoc.Figure(
      pandoc.Plain{image},
      dgr_opt.caption,
      dgr_opt['fig-attr']
    ) or
    pandoc.Plain{image}
end

function github_alerts (block)
  local para = block.content[1]
  if not para then return nil end
  local str = para.content[1]
  if not str or not str.text then return nil end

  local new_alert = function (icon, name)
    return pandoc.Span({
      pandoc.Span(icon, {class="icon"}),
      pandoc.Span(name, {class="alert"}),
    }, {class="alerting "..name})
  end

  local i = string.lower(str.text)
  if i == '[!note]' then
    t = new_alert("✏️", "Note")
  elseif i == '[!tip]' then
    t = new_alert("💡", "Tip")
  elseif i == '[!important]' then
    t = new_alert("⚠️", "Important")
  elseif i == '[!warning]' then
    t = new_alert("⛔", "Warning")
  elseif i == '[!caution]' then
    t = new_alert("🚨", "Caution")
  else
    return nil
  end
  table.remove(block.content[1].content, 1)
  table.remove(block.content[1].content, 1)
  table.insert(block.content, 1, t)

  return block
end

return {{
  CodeBlock = code_to_figure,
  Blocks = get_description,
  BlockQuote = github_alerts,
  Meta = function (meta)
    local r = ''
    local p = PANDOC_STATE.input_files[1]
    if p ~= "/dev/null" then
      local _, c = p:gsub("/", "")
      for i=1,c do r = r .. "../" end
    else
      r = '/'
    end
    meta.root = r
    if meta.desc ~= nil then
      meta.description = meta.desc
    end
    if meta.description == nil then
      meta.description = description
    end
    meta.runes = string.format("%0.1f", runes/1000)
    meta.read_time = string.format("%0.1f", runes/400)

    local p = PANDOC_STATE.input_files[1]
    local h = io.popen('stat -c %Y ' .. p)
    local r = h:read("*a")
    h:close()
    meta.updated = r:match("%d+")

    meta.path = "/"..p:sub(1,-3).."html"

    local envs = pandoc.system.environment()
    for k,v in pairs(envs) do
      if meta[k] == nil then
        meta[k] = v
      end
    end
    return meta
  end,
  Image = function (img)
    img.attributes.loading = "lazy"
    return img
  end,
  Link = function (link)
    local t = link.target
    if t:sub(1,4) ~= "http" and t:sub(-3) == ".md" then
      link.target = t:sub(1,-3).."html"
    end
    if #link.content == 0 then
      link.content = pandoc.List:new({pandoc.Str(link.target)})
    end
    return link
  end,
}}
