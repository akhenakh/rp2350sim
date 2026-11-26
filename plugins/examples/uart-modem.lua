-- A tiny serial companion: answer AT commands and emit one unsolicited event.
-- Replace the command set with the nRF host protocol you need to mock.
return {
  new = function(config)
    print("uart modem plugin ready")
    local d = { line = "", ticks = 0 }
    function d:uart_tx(b)
      if b == 13 or b == 10 then
        if d.line == "AT" then uart_send("OK\r\n")
        elseif d.line == "AT+VER?" then uart_send("rp2350sim-mock 1.0\r\n")
        elseif d.line ~= "" then uart_send("ERROR\r\n") end
        d.line = ""
      else
        d.line = d.line .. string.char(b)
      end
    end
    function d:tick(us)
      d.ticks = d.ticks + 1
      if d.ticks % 1000000 == 0 then uart_send("\r\nevt ready\r\n") end
    end
    return d
  end,
}
