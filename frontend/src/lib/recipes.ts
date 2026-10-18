// Starting points for the builder: each recipe presets protocol, port, algorithm, checks and nodes.
import type { BackendNode } from "@/types"
import type { Kind } from "./lbModel"

export type Check = "dns" | "http" | "https" | "tcp" | "udp" | "none"

export interface Recipe {
  key: "site" | "dns" | "doh" | "tcp" | "udp"
  label: string
  desc: string
  kind: Kind
  port: number
  algorithm: string
  check: Check
  name: string
  acme?: boolean
  redirect?: boolean
  nodes: BackendNode[]
}

export function blankNode(overrides: Partial<BackendNode> = {}): BackendNode {
  return {
    name: "",
    address: "",
    port: "",
    weight: 1,
    enabled: true,
    backup: false,
    max_conns: 0,
    tls_enabled: false,
    sni: "",
    hc_enabled: true,
    hc_protocol: "tcp",
    hc_port: "",
    hc_path: "/",
    hc_interval: 10,
    hc_timeout: 5,
    hc_failure_threshold: 3,
    hc_recovery_threshold: 2,
    ...overrides,
  }
}

export const recipeList: Recipe[] = [
  {
    key: "site", label: "Сайт или API", desc: "HTTPS с Let's Encrypt и переадресацией с 80-го порта",
    kind: "https_term", port: 443, algorithm: "round_robin", check: "http", name: "Сайт", acme: true, redirect: true,
    nodes: [blankNode({ name: "web1", port: 8080 }), blankNode({ name: "web2", port: 8080 })],
  },
  {
    key: "dns", label: "DNS-сервер", desc: "UDP и TCP на 53-м порту, ответы по DNS ID, проверка DNS-запросом",
    kind: "dns", port: 53, algorithm: "failover", check: "dns", name: "DNS",
    nodes: [blankNode({ name: "DNS1", port: 53 }), blankNode({ name: "DNS2", port: 53, backup: true })],
  },
  {
    key: "doh", label: "DNS-over-HTTPS", desc: "TLS на балансировщике, узлы AdGuard по HTTPS",
    kind: "https_term", port: 443, algorithm: "failover", check: "dns", name: "DoH", acme: true,
    nodes: [
      blankNode({ name: "DNS1", port: 443, tls_enabled: true, hc_port: 53 }),
      blankNode({ name: "DNS2", port: 443, tls_enabled: true, hc_port: 53, backup: true }),
    ],
  },
  {
    key: "tcp", label: "TCP-проброс", desc: "DoT, SSH, базы данных — TLS не расшифровывается",
    kind: "tcp", port: 853, algorithm: "least_conn", check: "tcp", name: "TCP",
    nodes: [blankNode({ name: "node1", port: 853 })],
  },
  {
    key: "udp", label: "UDP-сервис", desc: "Игры, VPN, QUIC — сессия на адрес клиента",
    kind: "udp", port: 51820, algorithm: "ip_hash", check: "udp", name: "UDP",
    nodes: [blankNode({ name: "node1", port: 51820 })],
  },
]
