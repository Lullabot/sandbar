// Exercise the prebuilt browser client shipped by the installed npm package.
// API-only checks miss a launch URL whose session key never reaches the UI.
const assert = require('node:assert/strict');
const { chromium } = require('playwright');

async function main() {
  const launchUrl = new URL(process.argv[2]);
  const capability = new URLSearchParams(launchUrl.hash.slice(1)).get('cap');
  assert.ok(capability, 'the browser launch URL needs the session key');

  const browser = await chromium.launch();
  try {
    const page = await browser.newPage();
    const loaded = Promise.all(['/api/config', '/api/diff'].map(path =>
      page.waitForResponse(response => new URL(response.url()).pathname === path)
    ));
    await page.goto(launchUrl.href);
    const [config, diff] = await loaded;
    for (const response of [config, diff]) {
      assert.equal(response.status(), 200, 'the browser must load authenticated APIs');
      assert.equal((await response.request().allHeaders()).authorization, `Bearer ${capability}`);
    }
    assert.ok((await config.json()).config);
    assert.ok((await diff.json()).diff.files.some(file =>
      (file.newPath || file.oldPath) === 'README.md'
    ));
    await page.getByText('README.md', { exact: true }).first().waitFor({ state: 'visible' });
    assert.equal(new URL(page.url()).hash, '', 'the client should remove the session key from the address bar');
    console.log('Browser loaded the review using the session key from sand’s URL');
  } finally {
    await browser.close();
  }
}

main().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
