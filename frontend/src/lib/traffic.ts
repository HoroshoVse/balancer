import { formatNumber } from "./lbModel"
import type { BackendNode, LoadBalancer } from "@/types"

export const nodeUploadMbps = (node: BackendNode) => (node.metrics?.upload_bps ?? 0) * 8 / 1_000_000
export const nodeDownloadMbps = (node: BackendNode) => (node.metrics?.download_bps ?? 0) * 8 / 1_000_000
export const nodeTotalMbps = (node: BackendNode) => nodeUploadMbps(node) + nodeDownloadMbps(node)
export const balancerTotalMbps = (lb: LoadBalancer) => (lb.backend_group?.backends ?? []).reduce((sum, node) => sum + nodeTotalMbps(node), 0)
export const formatMbps = (rate: number) => formatNumber(rate, rate < 10 ? 2 : 1)
