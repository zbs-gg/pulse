// GitHub release assets redirect to a signed, expiring CDN URL. The signed
// catalog still pins the original asset and its exact bytes; no credentials
// are needed or forwarded by the public installer.
export function isGitHubReleaseAssetURL(value) {
  try {
    const url = new URL(value);
    return url.origin === 'https://github.com' && !url.username && !url.password &&
      !url.search && !url.hash &&
      /^\/zbs-gg\/pulse\/releases\/download\/v\d+\.\d+\.\d+\/[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(url.pathname);
  } catch { return false; }
}

export function isGitHubReleaseAssetRedirect(source, destination) {
  if (!isGitHubReleaseAssetURL(source)) return false;
  try {
    const url = new URL(destination);
    return url.origin === 'https://release-assets.githubusercontent.com' &&
      !url.username && !url.password && !url.hash &&
      /^\/github-production-release-asset\/[a-zA-Z0-9/_-]+$/.test(url.pathname);
  } catch { return false; }
}
