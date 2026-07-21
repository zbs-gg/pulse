export class RemoteAuthError extends Error {
  constructor(code) {
    super(`remote_auth_${code}`);
    this.name = 'RemoteAuthError';
    this.code = code;
  }
}

export function remoteAuthFail(code) {
  throw new RemoteAuthError(code);
}
