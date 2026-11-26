-- Air-quality sensor mock on a UART. The firmware sends 'I' for the identity
-- and 'R' for a reading; the response is the same ASCII protocol as the
-- compiled WASM example, so the two can be swapped in boards.yaml.
return {
  new = function(config)
    print("aq-sensor lua ready")
    local d = { ticks = 0, pm = 12, co2 = 650, temp = 2240, hum = 4850 }
    function d:uart_tx(b)
      if b == 73 then -- 'I'
        uart_send("AQ-SENSOR 1.0\r\n")
      elseif b == 82 then -- 'R'
        uart_send(string.format("AQ,%d,%d,%d,%d\r\n", d.pm, d.co2, d.temp, d.hum))
      end
    end
    function d:tick(us)
      d.ticks = d.ticks + 1
      if d.ticks % 250 ~= 0 then return end -- four updates per second
      local t = d.ticks / 1000
      d.pm = math.floor(14 + 9 * math.sin(t * 0.7))
      d.co2 = math.floor(650 + 180 * math.sin(t * 0.35 + 0.5))
      d.temp = math.floor(2240 + 90 * math.sin(t * 0.2))
      d.hum = math.floor(4850 + 250 * math.sin(t * 0.3 + 1))
    end
    return d
  end,
}
