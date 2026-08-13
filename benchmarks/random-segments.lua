-- Reproducible 80/20 workload across three 15-minute videos.
-- Each video has 225 four-second segments. Segment numbers 1-45 are the hot
-- 20%; 46-225 are the remaining 80%.
local thread_id = 0
local videos = { "baseline", "baseline2", "baseline3" }

function setup(thread)
  thread:set("thread_id", thread_id)
  thread_id = thread_id + 1
end

function init(args)
  math.randomseed(42 + thread_id)
end

request = function()
  local video = videos[math.random(#videos)]
  local segment
  if math.random(100) <= 80 then
    segment = math.random(1, 45)
  else
    segment = math.random(46, 225)
  end

  local filename = string.format("chunk-0-%05d.m4s", segment)
  return wrk.format("GET", "/content/" .. video .. "/" .. filename)
end
