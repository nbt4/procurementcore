const PROCUREMENT_MOUNT_PATH = '/procurementcore'

export const appBasePath = window.location.pathname === PROCUREMENT_MOUNT_PATH
  || window.location.pathname.startsWith(`${PROCUREMENT_MOUNT_PATH}/`)
  ? PROCUREMENT_MOUNT_PATH
  : ''

export function appPath(path: string): string {
  const normalized = path.startsWith('/') ? path : `/${path}`
  return `${appBasePath}${normalized}`
}

export function appAssetPath(value: string): string {
  if (!appBasePath || !value || !value.startsWith('/') || value.startsWith(`${appBasePath}/`)) return value
  return appPath(value)
}

export const dashboardURL = appBasePath ? '/' : (window.__DASHBOARD_URL__ || '/')
