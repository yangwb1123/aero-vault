// Navigation primitive for the CDP delivery gate: no retries or weakened
// findings. Observe navigation and the existing load deadline together so a
// stalled command cannot crash Node via an unhandled load-event rejection.
export async function navigateAndLoad(cdp, sessionId, url, timeoutMs) {
  const loadEvent = cdp.once('Page.loadEventFired', sessionId, timeoutMs)
  const navigation = Promise.resolve().then(() => cdp.send('Page.navigate', { url }, sessionId))
  await Promise.all([navigation, loadEvent])
}
