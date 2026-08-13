import { test } from '@playwright/test';

import { mockApi, seedAuth } from './fixtures/recs';

const PATHS = [
  '/',
  '/home',
  '/opportunities',
  '/plans',
  '/purchases',
  '/inventory',
  '/inventory/coverage',
  '/admin',
  '/admin/users',
  '/dashboard',
  '/recommendations',
  '/history',
  '/settings',
  '/ri-exchange',
];

for (const p of PATHS) {
  test(`audit ${p}`, async ({ page }) => {
    const msgs: string[] = [];
    page.on('pageerror', (e) => msgs.push(`[pageerror] ${e.message}`));
    await mockApi(page);
    await seedAuth(page);
    await page.goto(p);
    await page.waitForTimeout(1500);
    const direct = await page.evaluate(() => ({
      title: document.title,
      path: window.location.pathname,
      tab: document.querySelector('.tab-content.active')?.id,
      btn: document.querySelector('.tab-btn.active')?.id,
    }));
    await page.reload();
    await page.waitForTimeout(1500);
    const refreshed = await page.evaluate(() => ({
      title: document.title,
      path: window.location.pathname,
      tab: document.querySelector('.tab-content.active')?.id,
      btn: document.querySelector('.tab-btn.active')?.id,
    }));
    console.log(`AUDIT ${p} direct=${JSON.stringify(direct)} refresh=${JSON.stringify(refreshed)} errs=${msgs.join('|')}`);
  });
}
