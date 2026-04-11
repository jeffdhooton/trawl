// trawl chromium stealth init script.
//
// Injected via page.AddScriptToEvaluateOnNewDocument before navigation
// when --stealth is set, so it runs in EVERY new document context
// (main frame + same-origin iframes) before any page script.
//
// Goal: pass the trivial "is this a headless browser?" checks that
// many sites still use (navigator.webdriver, empty plugins list,
// missing window.chrome, headless WebGL strings). Sophisticated
// detectors have counter-patches for every published stealth lib —
// see EVASION.md §5.2 for the explicit decision rule.
//
// Patches are intentionally idempotent and survive `delete` attempts
// where it cheaply can. We avoid try/catch around innocuous setters
// so any failure surfaces in the browser console for diagnosis.

(() => {
  // 1. navigator.webdriver: undefined matches a real browser, "true"
  //    is the dead-giveaway headless tell.
  Object.defineProperty(navigator, 'webdriver', {
    get: () => undefined,
    configurable: true,
  });

  // 2. navigator.plugins: real browsers expose at least the PDF
  //    viewer plugins. An empty list is the most common bot tell.
  const fakePlugins = [
    {
      name: 'PDF Viewer',
      filename: 'internal-pdf-viewer',
      description: 'Portable Document Format',
    },
    {
      name: 'Chrome PDF Viewer',
      filename: 'internal-pdf-viewer',
      description: 'Portable Document Format',
    },
    {
      name: 'Chromium PDF Viewer',
      filename: 'internal-pdf-viewer',
      description: 'Portable Document Format',
    },
  ];
  Object.defineProperty(navigator, 'plugins', {
    get: () => fakePlugins,
    configurable: true,
  });
  Object.defineProperty(navigator, 'mimeTypes', {
    get: () => [{ type: 'application/pdf', suffixes: 'pdf' }],
    configurable: true,
  });

  // 3. navigator.languages: headless Chrome historically returned an
  //    empty list. A two-entry list is the realistic baseline.
  Object.defineProperty(navigator, 'languages', {
    get: () => ['en-US', 'en'],
    configurable: true,
  });

  // 4. window.chrome: headless Chrome stubs this to a near-empty
  //    object. Real Chrome exposes a richer surface; the runtime sub-
  //    object is the one detectors hit most often.
  if (!window.chrome) {
    window.chrome = {};
  }
  if (!window.chrome.runtime) {
    window.chrome.runtime = {};
  }

  // 5. Notification permission: headless reports "denied" while real
  //    browsers default to "default". Detectors compare against the
  //    permissions API to spot the inconsistency.
  const origQuery = window.navigator.permissions && window.navigator.permissions.query;
  if (origQuery) {
    window.navigator.permissions.query = (parameters) =>
      parameters && parameters.name === 'notifications'
        ? Promise.resolve({ state: Notification.permission })
        : origQuery(parameters);
  }

  // 6. WebGL vendor / renderer: headless reports "Google Inc." +
  //    "Google SwiftShader" — the SwiftShader string is the giveaway.
  //    Override getParameter on both WebGL contexts to return realistic
  //    values matching a desktop integrated GPU.
  const patchWebGL = (proto) => {
    if (!proto) return;
    const orig = proto.getParameter;
    proto.getParameter = function (parameter) {
      // UNMASKED_VENDOR_WEBGL = 37445, UNMASKED_RENDERER_WEBGL = 37446
      if (parameter === 37445) return 'Intel Inc.';
      if (parameter === 37446) return 'Intel Iris OpenGL Engine';
      return orig.call(this, parameter);
    };
  };
  patchWebGL(window.WebGLRenderingContext && window.WebGLRenderingContext.prototype);
  patchWebGL(window.WebGL2RenderingContext && window.WebGL2RenderingContext.prototype);

  // 7. iframe contentWindow: some detectors create an iframe and
  //    check that its contentWindow.chrome equals the parent's. The
  //    headless stub fails this. Re-attach on iframe creation.
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
})();
