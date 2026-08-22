import { createSkinEngine, localStorageSkinStorage } from '@iris-ui-kit/react'

export const vaultSkin = createSkinEngine({
  skins: [],
  default: 'light',
  storage: localStorageSkinStorage('aero-vault-web:skin'),
})
