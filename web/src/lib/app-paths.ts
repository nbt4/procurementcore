const PROCUREMENT_MOUNT_PATH = "/procurementcore";
const browserWindow = typeof window === "undefined" ? undefined : window;

export const appBasePath =
  browserWindow?.location.pathname === PROCUREMENT_MOUNT_PATH ||
  browserWindow?.location.pathname.startsWith(`${PROCUREMENT_MOUNT_PATH}/`)
    ? PROCUREMENT_MOUNT_PATH
    : "";

export function appPath(path: string): string {
  const normalized = path.startsWith("/") ? path : `/${path}`;
  return `${appBasePath}${normalized}`;
}

export function appAssetPath(value: string): string {
  if (
    !appBasePath ||
    !value ||
    !value.startsWith("/") ||
    value.startsWith(`${appBasePath}/`)
  )
    return value;
  return appPath(value);
}

export function catalogProductPath(productId: number): string {
  return `/catalog/${productId}`;
}

export const dashboardURL = appBasePath
  ? "/"
  : browserWindow?.__DASHBOARD_URL__ || "/";

export const warehouseCoreURL =
  browserWindow?.__WAREHOUSECORE_URL__ ||
  (appBasePath ? "/warehousecore" : "http://localhost:8082");

export function warehouseProductsURL(
  params?: Record<string, string | number>,
): string {
  const base = warehouseCoreURL.endsWith("/")
    ? warehouseCoreURL
    : `${warehouseCoreURL}/`;
  const url = new URL(
    "products",
    base.startsWith("/")
      ? (browserWindow?.location.origin || "http://localhost") + base
      : base,
  );
  Object.entries(params || {}).forEach(([key, value]) =>
    url.searchParams.set(key, String(value)),
  );
  if (base.startsWith("/")) return `${url.pathname}${url.search}`;
  return url.toString();
}
