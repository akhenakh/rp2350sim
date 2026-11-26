-- Minimal SSD1306 command/data acceptor. The preset firmware sends a display
-- on command (control byte 0x00, command 0xAF); the model only needs to ACK.
return {
  new = function(config)
    print("ssd1306 plugin ready")
    local d = { data = false }
    function d:i2c_begin(addr) d.data = false end
    function d:i2c_write(b)
      if not d.data then d.data = true end
      return true
    end
    function d:i2c_read() return 0x00, true end
    function d:i2c_end() end
    return d
  end,
}
