// webframe.js paces the Shirei browser frame loop.
//
// The emulator and the UI share the browser's single thread. Shirei normally
// rebuilds the whole widget tree on every requestAnimationFrame, so a heavy
// static tab (Board, OTP, a large debug view) starves the interpreter and the
// demo falls below real time. The app sets shireiFrameInterval when the visible
// tab changes; this shim builds no faster than that, while pointer/key events
// still wake one frame immediately so clicks and typing stay responsive.
(function () {
  "use strict";

  var nativeRAF = window.requestAnimationFrame.bind(window);
  var queue = [];
  var timer = 0;
  var running = false;
  var last = 0;
  var urgent = false;

  window.shireiFrameInterval = 16;

  function directMode() {
    return (Number(window.shireiFrameInterval) || 16) <= 16;
  }

  function nextDelay() {
    if (urgent) return 0;
    var interval = Number(window.shireiFrameInterval) || 16;
    return Math.max(0, interval - (performance.now() - last));
  }

  function schedule(delay) {
    clearTimeout(timer);
    timer = setTimeout(runOne, Math.max(0, delay));
  }

  function runOne() {
    if (running || queue.length === 0) return;
    running = true;
    urgent = false;
    last = performance.now();
    var cb = queue.shift();
    nativeRAF(function (ts) {
      try {
        cb(ts);
      } catch (err) {
        console.error(err);
      }
      running = false;
      if (queue.length) {
        if (directMode()) nativeRAF(queue.shift());
        else schedule(nextDelay());
      }
    });
  }

  window.requestAnimationFrame = function (cb) {
    if (directMode()) return nativeRAF(cb);
    queue.push(cb);
    if (!running) {
      clearTimeout(timer);
      schedule(nextDelay());
    }
    return 1;
  };

  // Shirei never cancels, but keep the API shape.
  window.cancelAnimationFrame = function () {};

  window.shireiSetFrameInterval = function (ms) {
    window.shireiFrameInterval = Math.max(1, Number(ms) || 16);
    if (running) return;
    if (directMode()) {
      if (!queue.length) return;
      clearTimeout(timer);
      nativeRAF(queue.shift());
      return;
    }
    if (queue.length) schedule(nextDelay());
  };

  function wake() {
    urgent = true;
    if (!running && queue.length) {
      clearTimeout(timer);
      runOne();
    }
  }

  var events = [
    "pointerdown", "pointermove", "pointerup", "pointercancel",
    "wheel", "keydown", "keyup", "touchstart", "touchmove", "touchend"
  ];
  for (var i = 0; i < events.length; i++) {
    window.addEventListener(events[i], wake, { capture: true, passive: true });
  }
})();
