-- Minimal MPU-6050 register model: WHO_AM_I at 0x75 and a register pointer.
return {
  new = function(config)
    print("mpu6050 plugin ready")
    local d = { ptr = 0, first = true, regs = {} }
    d.regs[0x75] = 0x68
    function d:i2c_begin(addr) d.first = true end
    function d:i2c_write(b)
      if d.first then d.ptr = b; d.first = false
      else d.regs[d.ptr] = b; d.ptr = (d.ptr + 1) % 128 end
      return true
    end
    function d:i2c_read()
      local v = d.regs[d.ptr] or 0
      d.ptr = (d.ptr + 1) % 128
      return v, true
    end
    function d:i2c_end() end
    return d
  end,
}
