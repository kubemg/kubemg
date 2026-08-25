// Applies the deck the operator chose before first paint, so a light deck
// never flashes dark on the way in. Kept as its own file rather than inline
// in index.html so the console's Content-Security-Policy can require
// script-src 'self' with no 'unsafe-inline' exception.
try {
  if (localStorage.getItem('kubemg.theme') === 'light') {
    document.documentElement.dataset.theme = 'light'
  }
} catch {
  /* Private browsing refuses storage; dark is the default anyway. */
}
