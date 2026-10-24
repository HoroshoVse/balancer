// Configuration advice shown on balancer cards, with one-click fixes where safe.
import type { LoadBalancer } from "@/types"
import { mapNodes } from "./lbActions"
import { kindOf } from "./lbModel"

export interface Advice {
  id: string
  tone: "danger" | "warn" | "info"
  title: string
  text: string
  /** Applied in place through the API. */
  fix?: { label: string; apply: (lb: LoadBalancer) => LoadBalancer }
  /** Needs the builder instead. */
  edit?: string
}

const checkName: Record<string, string> = { tcp: "подключением TCP", udp: "пакетом UDP", http: "HTTP-запросом", https: "HTTPS-запросом" }

export function adviceFor(lb: LoadBalancer): Advice[] {
  const kind = kindOf(lb)
  const nodes = lb.backend_group?.backends ?? []
  const enabled = nodes.filter((b) => b.enabled)
  const checked = nodes.filter((b) => b.hc_enabled)
  const dnsChecked = checked.some((b) => b.hc_protocol === "dns")
  const out: Advice[] = []

  if ((lb.acme_enabled || lb.auto_ssl) && lb.acme_status === "error") {
    out.push({
      id: "acme-error",
      tone: "danger",
      title: "Не удалось загрузить или выпустить сертификат",
      text: `Сервер сам повторяет попытки и чинит повреждённые файлы сертификата. Ошибка: ${lb.acme_error || "неизвестна"}`,
    })
  }

  if (nodes.length > 0 && checked.length === 0) {
    const proto = kind === "dns" ? "dns" : kind === "udp" ? "udp" : kind === "http" || kind === "https_term" ? "http" : "tcp"
    out.push({
      id: "no-checks",
      tone: "warn",
      title: "Узлы не проверяются",
      text: "Упавший узел продолжит получать трафик, пока его не выключат вручную.",
      fix: { label: "Включить проверку", apply: (x) => mapNodes(x, (b) => ({ ...b, hc_enabled: true, hc_protocol: proto, hc_interval: b.hc_interval || 10 })) },
    })
  } else if (kind === "dns" && checked.some((b) => b.hc_protocol !== "dns")) {
    const current = checked.find((b) => b.hc_protocol !== "dns")?.hc_protocol ?? "tcp"
    out.push({
      id: "dns-check",
      tone: "warn",
      title: `Проверка ${checkName[current] ?? current} не видит зависший DNS`,
      text: "Порт может отвечать, даже когда сервер завис. Проверяйте настоящим DNS-запросом.",
      fix: { label: "Проверять DNS-запросом", apply: (x) => mapNodes(x, (b) => ({ ...b, hc_enabled: true, hc_protocol: "dns", hc_path: "" })) },
    })
  }

  if (lb.proxy_protocol_enabled && (kind === "dns" || dnsChecked)) {
    out.push({
      id: "proxy-adguard",
      tone: "warn",
      title: "AdGuard Home не понимает PROXY protocol",
      text: "Если узлы — AdGuard Home, запросы с этим заголовком обрываются.",
      fix: { label: "Выключить PROXY", apply: (x) => ({ ...x, proxy_protocol_enabled: false }) },
    })
  }

  if (kind === "https_term" && !lb.acme_enabled && !lb.auto_ssl && !lb.cert_data && !lb.cert_path) {
    out.push({ id: "self-signed", tone: "warn", title: "Сертификат самоподписанный", text: "Браузеры и DoH-клиенты будут ругаться. Включите Let's Encrypt или загрузите свой.", edit: "tls" })
  }

  // A private listen address proves nothing: hosters often map a public IP onto it
  if ((kind === "dns" || (kind === "udp" && lb.listen_port === 53)) && !lb.access_mode && !lb.access_deny?.trim()) {
    out.push({
      id: "open-dns",
      tone: "info",
      title: "Любой адрес в мире может слать запросы",
      text: "Открытый DNS-сервер используют для атак и перебора случайных имён. Если им пользуются из одной страны, оставьте только её.",
      edit: "access",
    })
  }

  if (enabled.length === 1) {
    out.push({ id: "single-node", tone: "info", title: "Всего один узел", text: "Если он откажет, запросы будет некуда отправить. Добавьте ещё один, хотя бы в резерв.", edit: "nodes" })
  }

  if (kind === "https_term" && dnsChecked) {
    out.push({
      id: "trusted-proxies",
      tone: "info",
      title: "AdGuard видит адрес балансировщика",
      text: "Добавьте IP балансировщика в trusted_proxies на узлах — в статистике AdGuard появятся настоящие клиенты.",
    })
  }
  return out
}
