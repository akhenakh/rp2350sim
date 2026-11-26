-- Minimal BME280 register model: pointer write, auto-increment reads and the
-- chip id at 0xD0. The same shape as the Go preset, in four methods.
return {
  new = function(config)
    print("bme280 plugin ready")
    local d = { ptr = 0, first = true, regs = {} }
    d.regs[0xD0] = 0x60
    d.regs[0xF4] = 0x00
    function d:i2c_begin(addr) d.first = true end
    function d:i2c_write(b)
      if d.first then d.ptr = b; d.first = false
      else d.regs[d.ptr] = b; d.ptr = (d.ptr + 1) % 256 end
      return true
    end
    function d:i2c_read()
      local v = d.regs[d.ptr] or 0
      d.ptr = (d.ptr + 1) % 256
      return v, true
    end
    function d:i2c_end() end
    return d
  end,
}
