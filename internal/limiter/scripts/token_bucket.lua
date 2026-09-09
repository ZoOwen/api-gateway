-- Token bucket rate limiter.
--
-- KEYS[1] = bucket key
-- ARGV[1] = capacity (burst)
-- ARGV[2] = refill rate, tokens per second (float)
-- ARGV[3] = requested tokens (always 1 for now)
-- ARGV[4] = key TTL in seconds
--
-- Returns {allowed, remaining, retry_after_ms, reset_ms} where
-- retry_after_ms and reset_ms are both relative to "now".

local capacity = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local requested = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

-- Time must come from Redis, never from the caller — clients disagree with
-- each other and with the server, which breaks the bucket math.
local time_parts = redis.call('TIME')
local now = tonumber(time_parts[1]) + tonumber(time_parts[2]) / 1000000

local bucket = redis.call('HMGET', KEYS[1], 'tokens', 'last_refill')
local tokens
local last_refill

if bucket[1] == false then
  tokens = capacity
  last_refill = now
else
  tokens = tonumber(bucket[1])
  last_refill = tonumber(bucket[2])
end

local elapsed = now - last_refill
if elapsed < 0 then
  elapsed = 0
end

tokens = math.min(capacity, tokens + elapsed * rate)

local allowed = 0
local retry_after_ms = 0

if tokens >= requested then
  tokens = tokens - requested
  allowed = 1
else
  local missing = requested - tokens
  if rate > 0 then
    retry_after_ms = math.ceil((missing / rate) * 1000)
  end
end

redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'last_refill', tostring(now))
redis.call('EXPIRE', KEYS[1], ttl)

local remaining = math.floor(tokens)

local missing_to_full = capacity - tokens
if missing_to_full < 0 then
  missing_to_full = 0
end

local reset_ms = 0
if rate > 0 then
  reset_ms = math.ceil((missing_to_full / rate) * 1000)
end

return {allowed, remaining, retry_after_ms, reset_ms}
