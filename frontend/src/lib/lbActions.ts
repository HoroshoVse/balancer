// Small in-place changes to a balancer without opening the builder.
import { api } from "@/lib/api"
import type { BackendNode, LoadBalancer } from "@/types"

/**
 * Saves the balancer with `mutate` applied. The API never returns the private
 * key, and an empty key keeps the stored one, so a round trip is safe.
 */
export async function updateLB(lb: LoadBalancer, mutate: (lb: LoadBalancer) => LoadBalancer) {
  await api("/load-balancers", { method: "PUT", json: mutate(lb) })
}

export const mapNodes = (lb: LoadBalancer, fn: (b: BackendNode) => BackendNode): LoadBalancer => ({
  ...lb,
  backend_group: { ...lb.backend_group, backends: (lb.backend_group?.backends ?? []).map(fn) },
})

export async function deleteLB(lb: LoadBalancer) {
  await api(`/load-balancers/delete?id=${lb.id}`, { method: "POST" })
}

export interface NodeTest {
  node: BackendNode
  ok: boolean
  logs: string
}

/** TCP (and TLS) reachability of a node from the balancer's host. */
type NodeTestOptions = Pick<LoadBalancer, "backend_http2_enabled" | "proxy_protocol_enabled" | "proxy_protocol_version">

export async function testNode(node: BackendNode, options: NodeTestOptions): Promise<NodeTest> {
  try {
    const res = await api<{ success: boolean; logs: string }>("/tools/test-connection", {
      method: "POST",
      json: {
        address: node.address, port: Number(node.port), tls_enabled: node.tls_enabled, sni: node.sni,
        http2_enabled: options.backend_http2_enabled,
        proxy_protocol_enabled: options.proxy_protocol_enabled,
        proxy_protocol_version: options.proxy_protocol_version,
      },
    })
    return { node, ok: res.success, logs: res.logs }
  } catch (err) {
    return { node, ok: false, logs: err instanceof Error ? err.message : String(err) }
  }
}
