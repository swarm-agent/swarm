// Only the runtime base class is stubbed; tests execute Fleet's real MCP methods.
export async function resolve(specifier, context, nextResolve) {
  if (specifier === 'cloudflare:workers') {
    return { url: 'data:text/javascript,export class DurableObject {}', shortCircuit: true };
  }
  return nextResolve(specifier, context);
}
