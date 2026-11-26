-- SPI NOR flash identification and status model. It is deliberately small:
-- enough for a JEDEC probe and a driver's first status read.
return {
  new = function(config)
    print("w25q16 plugin ready")
    local d = { cmd = 0, out = {} }
    function d:spi_select(active)
      if active then d.cmd = 0; d.out = {} end
    end
    function d:spi_transfer(tx)
      if d.cmd == 0 then
        d.cmd = tx
        if tx == 0x9F then d.out = {0x40, 0x15}; return 0xEF end
        if tx == 0x05 then return 0x00 end
        if tx == 0xAB then return 0x13 end
        return 0xFF
      end
      local v = table.remove(d.out, 1)
      if v == nil then v = 0x00 end
      return v
    end
    return d
  end,
}
