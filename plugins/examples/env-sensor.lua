-- Environment sensor mock: a register-level I2C device with temperature,
-- humidity and pressure. The firmware reads a register pointer then an
-- auto-incrementing block, exactly like a BME280-style part. Values drift so a
-- dashboard looks alive.
return {
  new = function(config)
    print("env-sensor lua ready")
    local d = { ptr = 0, first = true, ticks = 0, regs = {} }
    d.regs[0x00] = 0x5A -- chip id
    d.regs[0x07] = 0x01 -- ready

    local function set16(reg, v)
      if v < 0 then v = v + 65536 end
      v = math.floor(v)
      d.regs[reg] = math.floor(v / 256) % 256
      d.regs[reg + 1] = v % 256
    end

    function d:i2c_begin(addr) d.first = true end
    function d:i2c_write(b)
      if d.first then
        d.ptr = b
        d.first = false
      else
        d.regs[d.ptr] = b
        d.ptr = (d.ptr + 1) % 256
      end
      return true
    end
    function d:i2c_read()
      local v = d.regs[d.ptr] or 0
      d.ptr = (d.ptr + 1) % 256
      return v, true
    end
    function d:i2c_end() end
    function d:tick(us)
      d.ticks = d.ticks + 1
      if d.ticks % 20 ~= 0 then return end
      local t = d.ticks / 1000 -- seconds of virtual time
      set16(0x01, 2200 + 180 * math.sin(t * 0.8))
      set16(0x03, 5200 + 900 * math.sin(t * 0.5 + 1))
      set16(0x05, 10120 + 70 * math.sin(t * 0.3 + 2))
    end
    return d
  end,
}
