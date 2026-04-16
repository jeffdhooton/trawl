// trawl chromium stealth init script.
//
// Injected via page.AddScriptToEvaluateOnNewDocument before navigation
// when --stealth is set, so it runs in EVERY new document context
// (main frame + same-origin iframes) before any page script.
//
// Goal: pass the "is this a headless browser?" checks that real
// anti-bot products (DataDome, PerimeterX/HUMAN, Imperva/Incapsula,
// Cloudflare Bot Fight Mode) actually use. We don't aim for full
// Sannysoft/CreepJS compliance — that's a moving target. We aim for
// the fingerprints a consumer would actually hit in the wild.
// See EVASION.md §5.2 for the decision rule.
//
// Each patch lives in its own labeled block wrapped in try/catch so
// one failure (e.g. a browser API being unavailable in a given
// context) doesn't cascade. Patches are idempotent and survive
// `delete` attempts where it cheaply can.

(() => {
  const _log = (...args) => {
    // Keep errors surfaceable in the browser console for diagnosis
    // without leaking to the page via console.log hooks.
    try { console.debug('[trawl-stealth]', ...args); } catch (_) {}
  };

  // Per-document seed drives deterministic noise within one page
  // render but varies across documents. Canvas and audio fingerprints
  // hashed twice in the same render must be equal (real browsers are
  // deterministic per context); across separate scrape sessions the
  // hash should vary so the same automation can't be re-identified.
  const docSeed = Math.floor(Math.random() * 0xffffffff) >>> 0;

  // -----------------------------------------------------------------
  // 1. navigator.webdriver — undefined matches a real browser;
  //    "true" is the dead-giveaway headless tell. Patch both the
  //    instance and the prototype so proto-walking detectors don't
  //    catch a native `true` on Navigator.prototype.
  // -----------------------------------------------------------------
  try {
    Object.defineProperty(navigator, 'webdriver', {
      get: () => undefined,
      configurable: true,
    });
    if (Navigator && Navigator.prototype) {
      Object.defineProperty(Navigator.prototype, 'webdriver', {
        get: () => undefined,
        configurable: true,
      });
    }
  } catch (e) { _log('webdriver:', e); }

  // -----------------------------------------------------------------
  // 2. navigator.plugins + navigator.mimeTypes — real browsers expose
  //    at least the PDF viewer plugins. An empty list is the most
  //    common bot tell.
  // -----------------------------------------------------------------
  try {
    const fakePlugins = [
      { name: 'PDF Viewer', filename: 'internal-pdf-viewer', description: 'Portable Document Format' },
      { name: 'Chrome PDF Viewer', filename: 'internal-pdf-viewer', description: 'Portable Document Format' },
      { name: 'Chromium PDF Viewer', filename: 'internal-pdf-viewer', description: 'Portable Document Format' },
    ];
    Object.defineProperty(navigator, 'plugins', {
      get: () => fakePlugins,
      configurable: true,
    });
    Object.defineProperty(navigator, 'mimeTypes', {
      get: () => [{ type: 'application/pdf', suffixes: 'pdf' }],
      configurable: true,
    });
  } catch (e) { _log('plugins:', e); }

  // -----------------------------------------------------------------
  // 3. navigator.languages — headless Chrome historically returned
  //    an empty list. A two-entry list is the realistic baseline.
  // -----------------------------------------------------------------
  try {
    Object.defineProperty(navigator, 'languages', {
      get: () => ['en-US', 'en'],
      configurable: true,
    });
  } catch (e) { _log('languages:', e); }

  // -----------------------------------------------------------------
  // 4. window.chrome — headless Chromium exposes an empty {} where
  //    real Chrome has .runtime, .app, .csi, .loadTimes. Detectors
  //    call chrome.app.getDetails() / chrome.loadTimes() and measure
  //    whether the return shapes match real Chrome. These shims
  //    return plausible structures, not real data.
  // -----------------------------------------------------------------
  try {
    if (!window.chrome) { window.chrome = {}; }
    if (!window.chrome.runtime) { window.chrome.runtime = {}; }
    if (!window.chrome.app) {
      // Real chrome.app has getDetails / getIsInstalled / runningState,
      // plus InstallState + RunningState enum-ish objects.
      window.chrome.app = {
        isInstalled: false,
        InstallState: {
          DISABLED: 'disabled',
          INSTALLED: 'installed',
          NOT_INSTALLED: 'not_installed',
        },
        RunningState: {
          CANNOT_RUN: 'cannot_run',
          READY_TO_RUN: 'ready_to_run',
          RUNNING: 'running',
        },
        getDetails: () => null,
        getIsInstalled: () => false,
        runningState: () => 'cannot_run',
      };
    }
    if (!window.chrome.csi) {
      // Deprecated, but still present in real Chrome. Detectors that
      // care call it and expect a non-empty object.
      window.chrome.csi = () => ({
        startE: Date.now(),
        onloadT: Date.now(),
        pageT: 0,
        tran: 15,
      });
    }
    if (!window.chrome.loadTimes) {
      // Also deprecated but still exposed by real Chrome. Return a
      // plausible shape; values are only checked for type, not range.
      const nowSec = () => Date.now() / 1000;
      window.chrome.loadTimes = () => ({
        requestTime: nowSec() - 0.5,
        startLoadTime: nowSec() - 0.4,
        commitLoadTime: nowSec() - 0.3,
        finishDocumentLoadTime: nowSec() - 0.2,
        finishLoadTime: nowSec() - 0.1,
        firstPaintTime: nowSec() - 0.05,
        firstPaintAfterLoadTime: 0,
        navigationType: 'Other',
        wasFetchedViaSpdy: true,
        wasNpnNegotiated: true,
        npnNegotiatedProtocol: 'h2',
        wasAlternateProtocolAvailable: false,
        connectionInfo: 'h2',
      });
    }
  } catch (e) { _log('chrome-object:', e); }

  // -----------------------------------------------------------------
  // 5. Permissions + Notification.permission — headless reports
  //    notifications="denied" while real browsers default to "default"
  //    before the user has been prompted. Extend the shim beyond
  //    notifications so detectors checking geolocation/camera/etc.
  //    also see sensible values.
  // -----------------------------------------------------------------
  try {
    const origQuery = window.navigator.permissions && window.navigator.permissions.query;
    if (origQuery) {
      const shimmed = new Set([
        'notifications', 'geolocation', 'camera', 'microphone',
        'clipboard-read', 'clipboard-write', 'midi',
      ]);
      window.navigator.permissions.query = (parameters) => {
        if (parameters && shimmed.has(parameters.name)) {
          // 'prompt' is the pre-interaction default for most APIs.
          return Promise.resolve({ state: 'prompt', status: 'prompt' });
        }
        return origQuery(parameters);
      };
    }
    // Notification.permission returns a DOMString; shim the getter
    // so headless-installed "denied" default reads as "default".
    if (typeof Notification !== 'undefined') {
      Object.defineProperty(Notification, 'permission', {
        get: () => 'default',
        configurable: true,
      });
    }
  } catch (e) { _log('permissions:', e); }

  // -----------------------------------------------------------------
  // 6. WebGL vendor / renderer — headless reports "Google Inc." +
  //    "Google SwiftShader". The SwiftShader string is the giveaway.
  //    Spoof both contexts to a realistic desktop integrated GPU.
  // -----------------------------------------------------------------
  try {
    const patchWebGL = (proto) => {
      if (!proto) return;
      const orig = proto.getParameter;
      proto.getParameter = function (parameter) {
        if (parameter === 37445) return 'Intel Inc.';
        if (parameter === 37446) return 'Intel Iris OpenGL Engine';
        return orig.call(this, parameter);
      };
    };
    patchWebGL(window.WebGLRenderingContext && window.WebGLRenderingContext.prototype);
    patchWebGL(window.WebGL2RenderingContext && window.WebGL2RenderingContext.prototype);
  } catch (e) { _log('webgl:', e); }

  // -----------------------------------------------------------------
  // 7. iframe contentWindow — some detectors create an iframe and
  //    compare its contentWindow.chrome to the parent's. Re-attach.
  // -----------------------------------------------------------------
  try {
    const origCreate = document.createElement.bind(document);
    document.createElement = function (tag) {
      const el = origCreate(tag);
      if (tag && tag.toLowerCase() === 'iframe') {
        Object.defineProperty(el, 'contentWindow', {
          get: function () {
            const cw = HTMLIFrameElement.prototype.__lookupGetter__('contentWindow').call(this);
            if (cw && !cw.chrome) cw.chrome = window.chrome;
            return cw;
          },
          configurable: true,
        });
      }
      return el;
    };
  } catch (e) { _log('iframe:', e); }

  // -----------------------------------------------------------------
  // 8. Canvas fingerprint noise — DataDome/PX/Imperva hash the output
  //    of canvas.toDataURL() and getImageData() to identify clients.
  //    Because headless renders a deterministic canvas (no GPU AA
  //    jitter), the same URL called twice produces the same hash.
  //    Real browsers do too — but a population of real browsers has
  //    thousands of distinct hashes, while automation clusters on a
  //    handful. We add one-bit noise to ~0.1% of pixels, deterministic
  //    within a document (same hash on retry) but variable across
  //    documents (different hash per scrape session).
  // -----------------------------------------------------------------
  try {
    // Mulberry32 PRNG — stable, tiny, good distribution for what we need.
    const mkRand = (seed) => {
      let s = seed >>> 0;
      return () => {
        s = (s + 0x6D2B79F5) >>> 0;
        let t = s;
        t = Math.imul(t ^ (t >>> 15), t | 1);
        t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
        return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
      };
    };
    const perturbImageData = (imgData) => {
      if (!imgData || !imgData.data) return imgData;
      const data = imgData.data;
      const rand = mkRand(docSeed ^ (data.length & 0xffffffff));
      for (let i = 0; i < data.length; i += 4) {
        if (rand() < 0.001) {
          // Flip least-significant bit of a random channel (R/G/B,
          // skip A). Imperceptible visually; breaks naive hashing.
          const ch = (rand() * 3) | 0;
          data[i + ch] = data[i + ch] ^ 1;
        }
      }
      return imgData;
    };

    // Capture originals before any patching so toDataURL's internal
    // getImageData/putImageData path doesn't go through our patched
    // versions (which would double-perturb via XOR and cancel out).
    const origGetImageData = CanvasRenderingContext2D.prototype.getImageData;
    const origPutImageData = CanvasRenderingContext2D.prototype.putImageData;
    const origToDataURL = HTMLCanvasElement.prototype.toDataURL;
    const origToBlob = HTMLCanvasElement.prototype.toBlob;

    // Track which canvases we've already written perturbed pixels to.
    // Without this, a second toDataURL on the same canvas would re-read
    // the already-perturbed pixels and XOR-flip them back to original —
    // breaking real-browser behavior where toDataURL is deterministic
    // within a context. WeakSet so GC'd canvases clean up naturally.
    const perturbedCanvases = new WeakSet();

    const perturbCanvas = (canvas) => {
      if (!canvas || canvas.width <= 0 || canvas.height <= 0) return;
      if (perturbedCanvases.has(canvas)) return; // already perturbed
      const ctx = canvas.getContext('2d');
      if (!ctx) return;
      try {
        const img = origGetImageData.call(ctx, 0, 0, canvas.width, canvas.height);
        perturbImageData(img);
        origPutImageData.call(ctx, img, 0, 0);
        perturbedCanvases.add(canvas);
      } catch (_) { /* tainted canvas etc. */ }
    };

    HTMLCanvasElement.prototype.toDataURL = function () {
      perturbCanvas(this);
      return origToDataURL.apply(this, arguments);
    };
    if (origToBlob) {
      HTMLCanvasElement.prototype.toBlob = function () {
        perturbCanvas(this);
        return origToBlob.apply(this, arguments);
      };
    }

    // The getImageData patch ensures callers that read raw pixel data
    // (instead of going through toDataURL/toBlob) ALSO see perturbed
    // results. Separately-seeded so the two code paths don't
    // XOR-cancel when the same canvas is read both ways.
    CanvasRenderingContext2D.prototype.getImageData = function (x, y, w, h) {
      const img = origGetImageData.call(this, x, y, w, h);
      perturbImageData(img);
      return img;
    };
  } catch (e) { _log('canvas:', e); }

  // -----------------------------------------------------------------
  // 9. Audio fingerprint noise — AnalyserNode.getFloatFrequencyData /
  //    getByteFrequencyData and AudioBuffer.getChannelData are the
  //    common audio-fingerprint surfaces. Add minuscule sinusoidal
  //    noise so the deterministic signal varies per document.
  // -----------------------------------------------------------------
  try {
    const audioShift = ((docSeed & 0xffff) / 0xffff) * 1e-7;

    if (typeof AnalyserNode !== 'undefined') {
      const origFloat = AnalyserNode.prototype.getFloatFrequencyData;
      if (origFloat) {
        AnalyserNode.prototype.getFloatFrequencyData = function (array) {
          origFloat.call(this, array);
          for (let i = 0; i < array.length; i++) {
            array[i] += Math.sin(i + audioShift * 1e7) * 1e-6;
          }
        };
      }
      const origByte = AnalyserNode.prototype.getByteFrequencyData;
      if (origByte) {
        AnalyserNode.prototype.getByteFrequencyData = function (array) {
          origByte.call(this, array);
          // Byte data is uint8; too coarse for sub-integer noise.
          // Instead, flip a handful of bits in predictable positions.
          for (let i = 7; i < array.length; i += 191) {
            array[i] = array[i] ^ 1;
          }
        };
      }
    }

    if (typeof AudioBuffer !== 'undefined') {
      const origGetChannel = AudioBuffer.prototype.getChannelData;
      if (origGetChannel) {
        AudioBuffer.prototype.getChannelData = function (ch) {
          const data = origGetChannel.call(this, ch);
          // Perturb a sparse subset so audio still plays naturally
          // but the raw buffer hash differs across sessions.
          for (let i = 4096; i < data.length; i += 8192) {
            data[i] += audioShift;
          }
          return data;
        };
      }
    }
  } catch (e) { _log('audio:', e); }

  // Note: window.outerWidth / outerHeight intentionally NOT patched.
  // Chromium exposes them as unforgeable [Replaceable] accessors on
  // the Window instance; defineProperty / __defineGetter__ are both
  // silently rejected. A real fix requires emulation via CDP's
  // Emulation.setDeviceMetricsOverride — deferred, not JS-level.

  // -----------------------------------------------------------------
  // 10. navigator.deviceMemory / hardwareConcurrency — real browsers
  //     expose rounded values (1/2/4/8 GB memory; 2/4/8/16 cores).
  //     Headless can leave these undefined, which is itself a signal.
  //     Default to 8/8 when missing; don't override real values.
  // -----------------------------------------------------------------
  try {
    if (!('deviceMemory' in navigator) || !navigator.deviceMemory) {
      Object.defineProperty(navigator, 'deviceMemory', {
        get: () => 8,
        configurable: true,
      });
    }
    if (!('hardwareConcurrency' in navigator) || !navigator.hardwareConcurrency) {
      Object.defineProperty(navigator, 'hardwareConcurrency', {
        get: () => 8,
        configurable: true,
      });
    }
  } catch (e) { _log('device-stats:', e); }
})();
