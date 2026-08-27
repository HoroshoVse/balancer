import { useEffect, useState } from "react"
import { Link } from "react-router-dom"
import { Card, CardContent } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { toast } from "sonner"

const API_BASE = () => `http://${window.location.hostname}:8080`
const authHeaders = () => ({
  "Authorization": `Bearer ${localStorage.getItem("token")}`,
  "Content-Type": "application/json"
})

interface BackendNode {
  name: string
  address: string
  port: number
  weight: number
  enabled: boolean
  backup: boolean
  max_conns: number
  tls_enabled?: boolean
  status?: string
  hc_enabled?: boolean
  hc_protocol?: string
  hc_port?: number
  hc_path?: string
  hc_interval?: number
  hc_timeout?: number
  hc_failure_threshold?: number
  hc_recovery_threshold?: number
}

const emptyBackend = (): BackendNode => ({
  name: "Node 1",
  address: "127.0.0.1",
  port: 8080,
  weight: 1,
  enabled: true,
  backup: false,
  max_conns: 0,
  tls_enabled: false,
  hc_enabled: true,
  hc_protocol: "http",
  hc_port: 0,
  hc_path: "/",
  hc_interval: 10,
  hc_timeout: 5,
  hc_failure_threshold: 3,
  hc_recovery_threshold: 2
})

const selectClass = "flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
const checkboxClass = "h-4 w-4 rounded border-gray-300 text-primary focus:ring-primary"

export default function LoadBalancers() {
  const [loadBalancers, setLoadBalancers] = useState<any[]>([])
  const [isCreateOpen, setIsCreateOpen] = useState(false)
  const [isEditOpen, setIsEditOpen] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)

  // --- Form State ---
  const [name, setName] = useState("")
  const [listenIp, setListenIp] = useState("0.0.0.0")
  const [listenPort, setListenPort] = useState(80)
  const [protocol, setProtocol] = useState("http")
  const [algorithm, setAlgorithm] = useState("round_robin")

  // SSL / TLS
  const [sslEnabled, setSslEnabled] = useState(false)
  const [acmeEnabled, setAcmeEnabled] = useState(false)
  const [acmeEmail, setAcmeEmail] = useState("")
  const [acmeDomains, setAcmeDomains] = useState("")
  const [certPath, setCertPath] = useState("")
  const [keyPath, setKeyPath] = useState("")
  const [certData, setCertData] = useState("")
  const [keyData, setKeyData] = useState("")

  // HTTP/3
  const [http3Enabled, setHttp3Enabled] = useState(false)
  const [backendHttp2Enabled, setBackendHttp2Enabled] = useState(false)

  // Proxy Protocol
  const [proxyProtocolEnabled, setProxyProtocolEnabled] = useState(false)
  const [proxyProtocolVersion, setProxyProtocolVersion] = useState(2)

  // Sticky Sessions
  const [stickyEnabled, setStickyEnabled] = useState(false)
  const [stickyType, setStickyType] = useState("ip")

  // Backend Nodes
  const [backends, setBackends] = useState<BackendNode[]>([emptyBackend()])

  const resetForm = () => {
    setName(""); setListenIp("0.0.0.0"); setListenPort(80)
    setProtocol("http"); setAlgorithm("round_robin")
    setSslEnabled(false); setAcmeEnabled(false); setAcmeEmail(""); setAcmeDomains("")
    setCertPath(""); setKeyPath("")
    setCertData(""); setKeyData("")
    setHttp3Enabled(false); setBackendHttp2Enabled(false)
    setProxyProtocolEnabled(false); setProxyProtocolVersion(2)
    setStickyEnabled(false); setStickyType("ip")
    setBackends([emptyBackend()])
  }

  // --- Backend node helpers ---
  const addBackend = () => setBackends([...backends, emptyBackend()])
  const removeBackend = (i: number) => setBackends(backends.filter((_, idx) => idx !== i))
  const updateBackend = (i: number, field: keyof BackendNode, value: any) => {
    const updated = [...backends]
    updated[i] = { ...updated[i], [field]: value }
    setBackends(updated)
  }

  const [testResults, setTestResults] = useState<Record<number, string>>({})
  const testBackendConnection = async (i: number) => {
    const b = backends[i]
    if (!b.address) return
    setTestResults({ ...testResults, [i]: "Testing..." })
    try {
      const res = await fetch(`${API_BASE()}/api/v1/tools/test-connection`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Authorization": `Bearer ${localStorage.getItem("token")}`
        },
        body: JSON.stringify({
          address: b.address,
          port: b.port,
          tls_enabled: b.tls_enabled || false,
          http2_enabled: backendHttp2Enabled
        })
      })
      const data = await res.json()
      setTestResults({ ...testResults, [i]: data.logs || "Unknown error" })
    } catch (e) {
      setTestResults({ ...testResults, [i]: "Network error trying to test connection" })
    }
  }

  // --- Fetch ---
  const fetchLoadBalancers = () => {
    fetch(`${API_BASE()}/api/v1/load-balancers`, {
      headers: { "Authorization": `Bearer ${localStorage.getItem("token")}` }
    })
      .then(res => {
        if (res.status === 401) {
          localStorage.removeItem("token")
          window.location.href = "/login"
        }
        return res.json()
      })
      .then(data => setLoadBalancers(data || []))
      .catch(console.error)
  }

  useEffect(() => { fetchLoadBalancers() }, [])

  // --- Create ---
  const handleCreate = async () => {
    if (!name.trim()) { toast.error("Name is required"); return }
    if (backends.filter(b => b.address.trim()).length === 0) {
      toast.error("Add at least one backend node"); return
    }

    const payload = {
      name,
      listen_ip: listenIp,
      listen_port: listenPort,
      protocol,
      algorithm,
      ssl_enabled: sslEnabled,
      auto_ssl: acmeEnabled, // Map auto_ssl for backward compatibility or future use
      cert_path: certPath,
      key_path: keyPath,
      cert_data: certData,
      key_data: keyData,
      acme_enabled: acmeEnabled,
      acme_email: acmeEmail,
      acme_domains: acmeDomains,
      http3_enabled: http3Enabled,
      backend_http2_enabled: backendHttp2Enabled,
      proxy_protocol_enabled: proxyProtocolEnabled,
      proxy_protocol_version: proxyProtocolVersion,
      sticky_sessions_enabled: stickyEnabled,
      sticky_session_type: stickyType,
      backend_group: {
        name: name + " Group",
        backends: backends.filter(b => b.address.trim()).map(b => ({
          name: b.name || b.address,
          address: b.address,
          port: b.port,
          weight: b.weight,
          enabled: b.enabled,
          backup: b.backup,
          max_conns: b.max_conns,
          tls_enabled: b.tls_enabled,
          hc_enabled: b.hc_enabled,
          hc_protocol: b.hc_protocol,
          hc_port: b.hc_port,
          hc_path: b.hc_path,
          hc_interval: b.hc_interval,
          hc_timeout: b.hc_timeout,
          hc_failure_threshold: b.hc_failure_threshold,
          hc_recovery_threshold: b.hc_recovery_threshold
        }))
      }
    }

    try {
      const res = await fetch(`${API_BASE()}/api/v1/load-balancers`, {
        method: "POST",
        headers: authHeaders(),
        body: JSON.stringify(payload)
      })
      if (!res.ok) throw new Error("Failed to create")
      toast.success("Load Balancer created!")
      setIsCreateOpen(false)
      resetForm()
      fetchLoadBalancers()
    } catch {
      toast.error("Error creating load balancer")
    }
  }

  // --- Edit / Update ---
  const openEditModal = (lb: any) => {
    setEditingId(lb.id)
    setName(lb.name || "")
    setListenIp(lb.listen_ip || "0.0.0.0")
    setListenPort(lb.listen_port || 80)
    setProtocol(lb.protocol || "http")
    setAlgorithm(lb.algorithm || "round_robin")
    setSslEnabled(lb.ssl_enabled)
    setAcmeEnabled(lb.acme_enabled || lb.auto_ssl)
    setCertPath(lb.cert_path || "")
    setKeyPath(lb.key_path || "")
    setCertData(lb.cert_data || "")
    setKeyData(lb.key_data || "")
    setAcmeEmail(lb.acme_email || "")
    setAcmeDomains(lb.acme_domains || "")
    setHttp3Enabled(lb.http3_enabled || false)
    setBackendHttp2Enabled(lb.backend_http2_enabled || false)
    setProxyProtocolEnabled(lb.proxy_protocol_enabled || false)
    setProxyProtocolVersion(lb.proxy_protocol_version || 2)
    setStickyEnabled(lb.sticky_sessions_enabled || false)
    setStickyType(lb.sticky_session_type || "ip")
    
    if (lb.backend_group) {
      if (lb.backend_group.backends && lb.backend_group.backends.length > 0) {
        setBackends(lb.backend_group.backends)
      } else {
        setBackends([emptyBackend()])
      }
    } else {
      setBackends([emptyBackend()])
    }
    
    setIsEditOpen(true)
  }

  const handleUpdate = async () => {
    if (!name.trim()) { toast.error("Name is required"); return }
    if (backends.filter(b => b.address.trim()).length === 0) {
      toast.error("Add at least one backend node"); return
    }
    if (editingId === null) return;

    const payload = {
      id: editingId,
      name,
      listen_ip: listenIp,
      listen_port: listenPort,
      protocol,
      algorithm,
      ssl_enabled: sslEnabled,
      auto_ssl: acmeEnabled, // Map auto_ssl
      cert_path: certPath,
      key_path: keyPath,
      cert_data: certData,
      key_data: keyData,
      acme_enabled: acmeEnabled,
      acme_email: acmeEmail,
      acme_domains: acmeDomains,
      http3_enabled: http3Enabled,
      backend_http2_enabled: backendHttp2Enabled,
      proxy_protocol_enabled: proxyProtocolEnabled,
      proxy_protocol_version: proxyProtocolVersion,
      sticky_sessions_enabled: stickyEnabled,
      sticky_session_type: stickyType,
      backend_group: {
        name: name + " Group",
        backends: backends.filter(b => b.address.trim()).map(b => ({
          name: b.name || b.address,
          address: b.address,
          port: b.port,
          weight: b.weight,
          enabled: b.enabled !== undefined ? b.enabled : true,
          backup: b.backup || false,
          max_conns: b.max_conns || 0,
          tls_enabled: b.tls_enabled || false,
          hc_enabled: b.hc_enabled,
          hc_protocol: b.hc_protocol,
          hc_port: b.hc_port,
          hc_path: b.hc_path,
          hc_interval: b.hc_interval,
          hc_timeout: b.hc_timeout,
          hc_failure_threshold: b.hc_failure_threshold,
          hc_recovery_threshold: b.hc_recovery_threshold
        }))
      }
    }

    try {
      const res = await fetch(`${API_BASE()}/api/v1/load-balancers/update`, {
        method: "POST",
        headers: authHeaders(),
        body: JSON.stringify(payload)
      })
      if (!res.ok) throw new Error("Failed to update")
      toast.success("Load Balancer updated!")
      setIsEditOpen(false)
      resetForm()
      fetchLoadBalancers()
    } catch {
      toast.error("Error updating load balancer")
    }
  }

  // --- Delete ---
  const handleDelete = async (id: number) => {
    if (!confirm("Are you sure you want to delete this Load Balancer?")) return
    try {
      const res = await fetch(`${API_BASE()}/api/v1/load-balancers/delete?id=${id}`, {
        method: "POST",
        headers: { "Authorization": `Bearer ${localStorage.getItem("token")}` }
      })
      if (!res.ok) throw new Error("Failed to delete")
      toast.success("Load Balancer deleted!")
      fetchLoadBalancers()
    } catch {
      toast.error("Error deleting load balancer")
    }
  }

  return (
    <div className="grid gap-4 md:gap-8">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold tracking-tight">Load Balancers</h2>

        <Dialog open={isCreateOpen} onOpenChange={(open) => { setIsCreateOpen(open); if (!open) resetForm() }}>
          <DialogTrigger asChild>
            <Button>Create Load Balancer</Button>
          </DialogTrigger>
          <DialogContent className="max-w-2xl max-h-[85vh] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Create New Load Balancer</DialogTitle>
            </DialogHeader>
            <div className="grid gap-6 py-4">

              {/* === BASIC SETTINGS === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Basic Settings</h3>
                <div className="grid gap-2">
                  <label className="text-sm font-medium">Name *</label>
                  <Input value={name} onChange={e => setName(e.target.value)} placeholder="e.g. Production HTTP" />
                </div>
                <div className="grid grid-cols-2 gap-4">
                  <div className="grid gap-2">
                    <label className="text-sm font-medium">Listen IP</label>
                    <Input value={listenIp} onChange={e => setListenIp(e.target.value)} placeholder="0.0.0.0" />
                  </div>
                  <div className="grid gap-2">
                    <label className="text-sm font-medium">Port</label>
                    <Input type="number" value={listenPort} onChange={e => setListenPort(parseInt(e.target.value) || 80)} />
                  </div>
                </div>
                <div className="grid grid-cols-2 gap-4">
                  <div className="grid gap-2">
                    <label className="text-sm font-medium">Protocol</label>
                    <select className={selectClass} value={protocol} onChange={e => setProtocol(e.target.value)}>
                      <option value="http">HTTP</option>
                      <option value="https">HTTPS</option>
                      <option value="tcp">TCP</option>
                      <option value="udp">UDP</option>
                    </select>
                  </div>
                  <div className="grid gap-2">
                    <label className="text-sm font-medium">Algorithm</label>
                    <select className={selectClass} value={algorithm} onChange={e => setAlgorithm(e.target.value)}>
                      <option value="round_robin">Round Robin</option>
                      <option value="least_conn">Least Connections</option>
                      <option value="ip_hash">IP Hash</option>
                      <option value="failover">Failover (Active-Passive)</option>
                    </select>
                  </div>
                </div>
              </div>

              {/* === PROXY PROTOCOL === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Proxy Protocol (Real IP)</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={proxyProtocolEnabled} onChange={e => setProxyProtocolEnabled(e.target.checked)} id="pp-enabled" />
                  <label htmlFor="pp-enabled" className="text-sm font-medium">Enable Proxy Protocol</label>
                </div>
                {proxyProtocolEnabled && (
                  <div className="grid gap-2 ml-7">
                    <label className="text-sm font-medium">Version</label>
                    <select className={selectClass} value={proxyProtocolVersion} onChange={e => setProxyProtocolVersion(parseInt(e.target.value))}>
                      <option value={1}>v1 (text)</option>
                      <option value={2}>v2 (binary, recommended)</option>
                    </select>
                    <p className="text-xs text-muted-foreground">Passes real client IP to backend servers through the PROXY protocol header.</p>
                  </div>
                )}
              </div>

              {/* === SSL / TLS === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">SSL / TLS</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={sslEnabled} onChange={e => setSslEnabled(e.target.checked)} id="ssl-enabled" />
                  <label htmlFor="ssl-enabled" className="text-sm font-medium">Enable SSL/TLS</label>
                </div>
                {sslEnabled && (
                  <div className="space-y-3 ml-7">
                    <div className="flex items-center gap-3">
                      <input type="checkbox" className={checkboxClass} checked={acmeEnabled} onChange={e => setAcmeEnabled(e.target.checked)} id="acme-enabled" />
                      <label htmlFor="acme-enabled" className="text-sm font-medium">Auto-SSL (Let's Encrypt / ACME)</label>
                    </div>
                    {acmeEnabled && (
                      <div className="grid gap-3 ml-7">
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">ACME Email</label>
                          <Input value={acmeEmail} onChange={e => setAcmeEmail(e.target.value)} placeholder="admin@example.com" />
                        </div>
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">Domains (Comma separated)</label>
                          <Input value={acmeDomains} onChange={e => setAcmeDomains(e.target.value)} placeholder="example.com, www.example.com" />
                        </div>
                      </div>
                    )}
                    {!acmeEnabled && (
                      <div className="grid gap-4 ml-7 mt-2">
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">Certificate (PEM string)</label>
                          <textarea className="flex min-h-[80px] w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" 
                            value={certData} onChange={e => setCertData(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----..." />
                        </div>
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">Private Key (PEM string)</label>
                          <textarea className="flex min-h-[80px] w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" 
                            value={keyData} onChange={e => setKeyData(e.target.value)} placeholder="-----BEGIN PRIVATE KEY-----..." />
                        </div>
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">OR Certificate Path (on server)</label>
                          <Input value={certPath} onChange={e => setCertPath(e.target.value)} placeholder="/app/certs/server.crt" />
                        </div>
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">OR Private Key Path (on server)</label>
                          <Input value={keyPath} onChange={e => setKeyPath(e.target.value)} placeholder="/app/certs/server.key" />
                        </div>
                        <p className="text-xs text-muted-foreground">Paste the certificate contents OR specify paths on the server. Leave empty for self-signed.</p>
                      </div>
                    )}
                  </div>
                )}
              </div>

              {/* === HTTP/3 === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">HTTP/3 (QUIC)</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={http3Enabled} onChange={e => setHttp3Enabled(e.target.checked)} id="h3-enabled" />
                  <label htmlFor="h3-enabled" className="text-sm font-medium">Enable HTTP/3</label>
                </div>
                <p className="text-xs text-muted-foreground ml-7">Enables QUIC-based HTTP/3 for faster connections. Requires SSL.</p>
              </div>

              {/* === Backend HTTP/2 === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Backend Connections</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={backendHttp2Enabled} onChange={e => setBackendHttp2Enabled(e.target.checked)} id="backend-h2-enabled" />
                  <label htmlFor="backend-h2-enabled" className="text-sm font-medium">Enable HTTP/2 to Backends</label>
                </div>
                <p className="text-xs text-muted-foreground ml-7">Required for DNS-over-HTTPS (DoH) servers like AdGuard Home. Disables HTTP/1.1 fallback.</p>
              </div>

              {/* === STICKY SESSIONS === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Sticky Sessions</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={stickyEnabled} onChange={e => setStickyEnabled(e.target.checked)} id="sticky-enabled" />
                  <label htmlFor="sticky-enabled" className="text-sm font-medium">Enable Sticky Sessions</label>
                </div>
                {stickyEnabled && (
                  <div className="grid gap-2 ml-7">
                    <label className="text-sm font-medium">Type</label>
                    <select className={selectClass} value={stickyType} onChange={e => setStickyType(e.target.value)}>
                      <option value="ip">Source IP</option>
                      <option value="cookie">Cookie</option>
                    </select>
                  </div>
                )}
              </div>

              {/* === BACKEND NODES === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Backend Nodes *</h3>
                {backends.map((b, i) => (
                  <div key={i} className="border rounded-lg p-4 space-y-3 relative">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium">Node #{i + 1}</span>
                        <Button variant="outline" size="sm" className="h-6 px-2 text-xs ml-2" onClick={() => testBackendConnection(i)}>Test Connection</Button>
                      </div>
                      {backends.length > 1 && (
                        <Button variant="ghost" size="sm" className="text-destructive h-6 px-2" onClick={() => removeBackend(i)}>✕ Remove</Button>
                      )}
                    </div>
                    {testResults[i] && (
                      <div className="text-xs bg-slate-900 text-slate-300 p-2 rounded whitespace-pre-wrap font-mono">
                        {testResults[i]}
                      </div>
                    )}
                    <div className="grid grid-cols-3 gap-3">
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Name</label>
                        <Input value={b.name} onChange={e => updateBackend(i, "name", e.target.value)} placeholder="App Node 1" />
                      </div>
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Address *</label>
                        <Input value={b.address} onChange={e => updateBackend(i, "address", e.target.value)} placeholder="192.168.1.10" />
                      </div>
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Port</label>
                        <Input type="number" value={b.port} onChange={e => updateBackend(i, "port", parseInt(e.target.value) || 80)} />
                      </div>
                    </div>
                    <div className="grid grid-cols-3 gap-3">
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Weight</label>
                        <Input type="number" value={b.weight} onChange={e => updateBackend(i, "weight", parseInt(e.target.value) || 1)} min={1} />
                      </div>
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Max Connections (0 = unlimited)</label>
                        <Input type="number" value={b.max_conns} onChange={e => updateBackend(i, "max_conns", parseInt(e.target.value) || 0)} min={0} />
                      </div>
                      <div className="flex items-end gap-2 flex-wrap pb-1">
                        <label className="flex items-center gap-2 text-xs">
                          <input type="checkbox" className={checkboxClass} checked={b.enabled} onChange={e => updateBackend(i, "enabled", e.target.checked)} />
                          Enabled
                        </label>
                        <label className="flex items-center gap-2 text-xs" title="Backup nodes only receive traffic when ALL primary nodes are down">
                          <input type="checkbox" className={checkboxClass} checked={b.backup} onChange={e => updateBackend(i, "backup", e.target.checked)} />
                          Backup (standby)
                        </label>
                        <label className="flex items-center gap-2 text-xs text-blue-500" title="Connect to this node via HTTPS instead of HTTP">
                          <input type="checkbox" className={checkboxClass} checked={b.tls_enabled || false} onChange={e => updateBackend(i, "tls_enabled", e.target.checked)} />
                          HTTPS
                        </label>
                      </div>
                    </div>
                  </div>
                ))}
                <Button variant="outline" onClick={addBackend} className="w-full">+ Add Backend Node</Button>
              </div>



              {/* === CREATE BUTTON === */}
              <Button onClick={handleCreate} className="w-full mt-2" size="lg">Create Load Balancer</Button>
            </div>
          </DialogContent>
        </Dialog>

        <Dialog open={isEditOpen} onOpenChange={(open) => { setIsEditOpen(open); if (!open) resetForm() }}>
          <DialogContent className="max-w-2xl max-h-[85vh] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Edit Load Balancer</DialogTitle>
            </DialogHeader>
            <div className="grid gap-6 py-4">

              {/* === BASIC SETTINGS === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Basic Settings</h3>
                <div className="grid gap-2">
                  <label className="text-sm font-medium">Name *</label>
                  <Input value={name} onChange={e => setName(e.target.value)} placeholder="e.g. Production HTTP" />
                </div>
                <div className="grid grid-cols-2 gap-4">
                  <div className="grid gap-2">
                    <label className="text-sm font-medium">Listen IP</label>
                    <Input value={listenIp} onChange={e => setListenIp(e.target.value)} placeholder="0.0.0.0" />
                  </div>
                  <div className="grid gap-2">
                    <label className="text-sm font-medium">Port</label>
                    <Input type="number" value={listenPort} onChange={e => setListenPort(parseInt(e.target.value) || 80)} />
                  </div>
                </div>
                <div className="grid grid-cols-2 gap-4">
                  <div className="grid gap-2">
                    <label className="text-sm font-medium">Protocol</label>
                    <select className={selectClass} value={protocol} onChange={e => setProtocol(e.target.value)}>
                      <option value="http">HTTP</option>
                      <option value="https">HTTPS</option>
                      <option value="tcp">TCP</option>
                      <option value="udp">UDP</option>
                    </select>
                  </div>
                  <div className="grid gap-2">
                    <label className="text-sm font-medium">Algorithm</label>
                    <select className={selectClass} value={algorithm} onChange={e => setAlgorithm(e.target.value)}>
                      <option value="round_robin">Round Robin</option>
                      <option value="least_conn">Least Connections</option>
                      <option value="ip_hash">IP Hash</option>
                      <option value="failover">Failover (Active-Passive)</option>
                    </select>
                  </div>
                </div>
              </div>

              {/* === PROXY PROTOCOL === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Proxy Protocol (Real IP)</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={proxyProtocolEnabled} onChange={e => setProxyProtocolEnabled(e.target.checked)} id="edit-pp-enabled" />
                  <label htmlFor="edit-pp-enabled" className="text-sm font-medium">Enable Proxy Protocol</label>
                </div>
                {proxyProtocolEnabled && (
                  <div className="grid gap-2 ml-7">
                    <label className="text-sm font-medium">Version</label>
                    <select className={selectClass} value={proxyProtocolVersion} onChange={e => setProxyProtocolVersion(parseInt(e.target.value))}>
                      <option value={1}>v1 (text)</option>
                      <option value={2}>v2 (binary, recommended)</option>
                    </select>
                    <p className="text-xs text-muted-foreground">Passes real client IP to backend servers through the PROXY protocol header.</p>
                  </div>
                )}
              </div>

              {/* === SSL / TLS === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">SSL / TLS</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={sslEnabled} onChange={e => setSslEnabled(e.target.checked)} id="edit-ssl-enabled" />
                  <label htmlFor="edit-ssl-enabled" className="text-sm font-medium">Enable SSL/TLS</label>
                </div>
                {sslEnabled && (
                  <div className="space-y-3 ml-7">
                    <div className="flex items-center gap-3">
                      <input type="checkbox" className={checkboxClass} checked={acmeEnabled} onChange={e => setAcmeEnabled(e.target.checked)} id="edit-acme-enabled" />
                      <label htmlFor="edit-acme-enabled" className="text-sm font-medium">Auto-SSL (Let's Encrypt / ACME)</label>
                    </div>
                    {acmeEnabled && (
                      <div className="grid gap-3 ml-7">
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">ACME Email</label>
                          <Input value={acmeEmail} onChange={e => setAcmeEmail(e.target.value)} placeholder="admin@example.com" />
                        </div>
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">Domains (Comma separated)</label>
                          <Input value={acmeDomains} onChange={e => setAcmeDomains(e.target.value)} placeholder="example.com, www.example.com" />
                        </div>
                      </div>
                    )}
                    {!acmeEnabled && (
                      <div className="grid gap-4 ml-7 mt-2">
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">Certificate (PEM string)</label>
                          <textarea className="flex min-h-[80px] w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" 
                            value={certData} onChange={e => setCertData(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----..." />
                        </div>
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">Private Key (PEM string)</label>
                          <textarea className="flex min-h-[80px] w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" 
                            value={keyData} onChange={e => setKeyData(e.target.value)} placeholder="-----BEGIN PRIVATE KEY-----..." />
                        </div>
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">OR Certificate Path (on server)</label>
                          <Input value={certPath} onChange={e => setCertPath(e.target.value)} placeholder="/app/certs/server.crt" />
                        </div>
                        <div className="grid gap-2">
                          <label className="text-sm font-medium">OR Private Key Path (on server)</label>
                          <Input value={keyPath} onChange={e => setKeyPath(e.target.value)} placeholder="/app/certs/server.key" />
                        </div>
                        <p className="text-xs text-muted-foreground">Paste the certificate contents OR specify paths on the server. Leave empty for self-signed.</p>
                      </div>
                    )}
                  </div>
                )}
              </div>

              {/* === HTTP/3 === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">HTTP/3 (QUIC)</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={http3Enabled} onChange={e => setHttp3Enabled(e.target.checked)} id="edit-h3-enabled" />
                  <label htmlFor="edit-h3-enabled" className="text-sm font-medium">Enable HTTP/3</label>
                </div>
                <p className="text-xs text-muted-foreground ml-7">Enables QUIC-based HTTP/3 for faster connections. Requires SSL.</p>
              </div>

              {/* === Backend HTTP/2 === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Backend Connections</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={backendHttp2Enabled} onChange={e => setBackendHttp2Enabled(e.target.checked)} id="edit-backend-h2-enabled" />
                  <label htmlFor="edit-backend-h2-enabled" className="text-sm font-medium">Enable HTTP/2 to Backends</label>
                </div>
                <p className="text-xs text-muted-foreground ml-7">Required for DNS-over-HTTPS (DoH) servers like AdGuard Home. Disables HTTP/1.1 fallback.</p>
              </div>

              {/* === STICKY SESSIONS === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Sticky Sessions</h3>
                <div className="flex items-center gap-3">
                  <input type="checkbox" className={checkboxClass} checked={stickyEnabled} onChange={e => setStickyEnabled(e.target.checked)} id="edit-sticky-enabled" />
                  <label htmlFor="edit-sticky-enabled" className="text-sm font-medium">Enable Sticky Sessions</label>
                </div>
                {stickyEnabled && (
                  <div className="grid gap-2 ml-7">
                    <label className="text-sm font-medium">Type</label>
                    <select className={selectClass} value={stickyType} onChange={e => setStickyType(e.target.value)}>
                      <option value="ip">Source IP</option>
                      <option value="cookie">Cookie</option>
                    </select>
                  </div>
                )}
              </div>

              {/* === BACKEND NODES === */}
              <div className="space-y-3">
                <h3 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground border-b pb-2">Backend Nodes *</h3>
                {backends.map((b, i) => (
                  <div key={i} className="border rounded-lg p-4 space-y-3 relative">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium">Node #{i + 1}</span>
                        <Button variant="outline" size="sm" className="h-6 px-2 text-xs ml-2" onClick={() => testBackendConnection(i)}>Test Connection</Button>
                      </div>
                      {backends.length > 1 && (
                        <Button variant="ghost" size="sm" className="text-destructive h-6 px-2" onClick={() => removeBackend(i)}>✕ Remove</Button>
                      )}
                    </div>
                    {testResults[i] && (
                      <div className="text-xs bg-slate-900 text-slate-300 p-2 rounded whitespace-pre-wrap font-mono">
                        {testResults[i]}
                      </div>
                    )}
                    <div className="grid grid-cols-3 gap-3">
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Name</label>
                        <Input value={b.name} onChange={e => updateBackend(i, "name", e.target.value)} placeholder="App Node 1" />
                      </div>
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Address *</label>
                        <Input value={b.address} onChange={e => updateBackend(i, "address", e.target.value)} placeholder="192.168.1.10" />
                      </div>
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Port</label>
                        <Input type="number" value={b.port} onChange={e => updateBackend(i, "port", parseInt(e.target.value) || 80)} />
                      </div>
                    </div>
                    <div className="grid grid-cols-3 gap-3">
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Weight</label>
                        <Input type="number" value={b.weight} onChange={e => updateBackend(i, "weight", parseInt(e.target.value) || 1)} min={1} />
                      </div>
                      <div className="grid gap-1">
                        <label className="text-xs text-muted-foreground">Max Connections (0 = unlimited)</label>
                        <Input type="number" value={b.max_conns} onChange={e => updateBackend(i, "max_conns", parseInt(e.target.value) || 0)} min={0} />
                      </div>
                      <div className="flex items-end gap-2 flex-wrap pb-1">
                        <label className="flex items-center gap-1 text-xs">
                          <input type="checkbox" className={checkboxClass} checked={b.enabled} onChange={e => updateBackend(i, "enabled", e.target.checked)} />
                          Enabled
                        </label>
                        <label className="flex items-center gap-1 text-xs" title="Backup nodes only receive traffic when ALL primary nodes are down">
                          <input type="checkbox" className={checkboxClass} checked={b.backup} onChange={e => updateBackend(i, "backup", e.target.checked)} />
                          Backup
                        </label>
                        <label className="flex items-center gap-1 text-xs text-blue-500" title="Connect to this node via HTTPS instead of HTTP">
                          <input type="checkbox" className={checkboxClass} checked={b.tls_enabled || false} onChange={e => updateBackend(i, "tls_enabled", e.target.checked)} />
                          HTTPS
                        </label>
                      </div>
                      
                      {/* Backend-level Health Checks */}
                      <div className="col-span-3 border-t pt-3 mt-2">
                        <div className="flex items-center gap-3 mb-2">
                          <input type="checkbox" className={checkboxClass} checked={b.hc_enabled ?? true} onChange={e => updateBackend(i, "hc_enabled", e.target.checked)} id={`edit-hc-enabled-${i}`} />
                          <label htmlFor={`edit-hc-enabled-${i}`} className="text-xs font-medium">Enable Health Checks</label>
                        </div>
                        {(b.hc_enabled ?? true) && (
                          <div className="grid grid-cols-4 gap-3">
                            <div className="grid gap-1">
                              <label className="text-xs text-muted-foreground">Protocol</label>
                              <select className={selectClass} value={b.hc_protocol || "http"} onChange={e => updateBackend(i, "hc_protocol", e.target.value)}>
                                <option value="http">HTTP</option>
                                <option value="tcp">TCP</option>
                                <option value="udp">UDP</option>
                              </select>
                            </div>
                            <div className="grid gap-1">
                              <label className="text-xs text-muted-foreground">Port</label>
                              <Input type="number" value={b.hc_port || 0} onChange={e => updateBackend(i, "hc_port", parseInt(e.target.value) || 0)} />
                            </div>
                            <div className="grid gap-1">
                              <label className="text-xs text-muted-foreground">Path</label>
                              <Input value={b.hc_path || "/"} onChange={e => updateBackend(i, "hc_path", e.target.value)} />
                            </div>
                            <div className="grid gap-1">
                              <label className="text-xs text-muted-foreground">Interval</label>
                              <Input type="number" value={b.hc_interval || 10} onChange={e => updateBackend(i, "hc_interval", parseInt(e.target.value) || 10)} />
                            </div>
                          </div>
                        )}
                      </div>
                    </div>
                  </div>
                ))}
                <Button variant="outline" onClick={addBackend} className="w-full">+ Add Backend Node</Button>
              </div>



              {/* === UPDATE BUTTON === */}
              <Button onClick={handleUpdate} className="w-full mt-2" size="lg">Update Load Balancer</Button>
            </div>
          </DialogContent>
        </Dialog>
      </div>

      {/* === TABLE === */}
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Address</TableHead>
                <TableHead>Protocol</TableHead>
                <TableHead>Algorithm</TableHead>
                <TableHead>Backends</TableHead>
                <TableHead>SSL</TableHead>
                <TableHead>Proxy Protocol</TableHead>
                <TableHead>Metrics</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {loadBalancers.map((lb) => (
                <TableRow key={lb.id}>
                  <TableCell className="font-medium">
                    <Link to={`/lbs/${lb.id}`} className="hover:underline text-blue-500">
                      {lb.name}
                    </Link>
                  </TableCell>
                  <TableCell>{lb.listen_ip}:{lb.listen_port}</TableCell>
                  <TableCell>
                    <Badge variant="outline">{lb.protocol?.toUpperCase()}</Badge>
                    {lb.http3_enabled && <Badge variant="secondary" className="ml-1">H3</Badge>}
                  </TableCell>
                  <TableCell>{lb.algorithm?.replace("_", " ")}</TableCell>
                  <TableCell>
                    <div className="flex flex-col gap-1">
                      <Badge variant="secondary">{lb.backend_group?.backends?.length || 0} nodes</Badge>
                      <div className="flex flex-wrap gap-1 mt-1">
                        {lb.backend_group?.backends?.map((b: any, i: number) => {
                           return (
                             <div key={b.id || i} className="flex items-center gap-1 text-[10px] text-muted-foreground" title={`${b.address}:${b.port}`}>
                               <div className={`w-2 h-2 rounded-full ${b.status === 'UP' ? 'bg-green-500' : (b.status === 'DOWN' ? 'bg-red-500' : 'bg-gray-500')}`} title={b.status || 'UNKNOWN'} />
                               {b.name || b.address}
                             </div>
                           )
                        })}
                      </div>
                    </div>
                  </TableCell>
                  <TableCell>
                    {lb.ssl_enabled ? (
                      lb.acme_enabled ? (
                        <Badge 
                          className={
                            lb.acme_status === "ok" ? "bg-green-500 hover:bg-green-600" :
                            lb.acme_status === "issuing" ? "bg-yellow-500 hover:bg-yellow-600" :
                            lb.acme_status === "error" ? "bg-red-500 hover:bg-red-600" :
                            ""
                          }
                          title={lb.acme_error || "Auto-SSL (ACME)"}
                        >
                          ACME {lb.acme_status === "issuing" ? "⏳" : lb.acme_status === "error" ? "⚠️" : ""}
                        </Badge>
                      ) : (
                        <Badge variant="default">SSL</Badge>
                      )
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </TableCell>
                  <TableCell>
                    {lb.proxy_protocol_enabled ? (
                      <Badge variant="outline">PROXYv{lb.proxy_protocol_version}</Badge>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center space-x-2">
                      <span className="text-xs text-muted-foreground">RPS: {lb.metrics?.total_requests || 0}</span>
                      <span className="text-xs text-muted-foreground">|</span>
                      <span className="text-xs text-muted-foreground">Err: {lb.metrics?.error_rate_percent?.toFixed(1) || 0}%</span>
                      <span className="text-xs text-muted-foreground">|</span>
                      <span className="text-xs text-muted-foreground">Lat: {lb.metrics?.average_latency_ms || 0}ms</span>
                    </div>
                  </TableCell>
                  <TableCell className="text-right space-x-2">
                    <Button variant="outline" size="sm" onClick={() => openEditModal(lb)}>Edit</Button>
                    <Button variant="destructive" size="sm" onClick={() => handleDelete(lb.id)}>Delete</Button>
                  </TableCell>
                </TableRow>
              ))}
              {loadBalancers.length === 0 && (
                <TableRow>
                  <TableCell colSpan={9} className="text-center py-8 text-muted-foreground">
                    No load balancers found. Create one to get started.
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}
