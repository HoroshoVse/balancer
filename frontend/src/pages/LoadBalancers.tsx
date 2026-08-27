import { useEffect, useState } from "react"
import { Card, CardContent } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { toast } from "sonner"

export default function LoadBalancers() {
  const [loadBalancers, setLoadBalancers] = useState<any[]>([])
  const [isCreateOpen, setIsCreateOpen] = useState(false)
  const [formData, setFormData] = useState({
    name: "",
    listen_ip: "0.0.0.0",
    listen_port: 80,
    protocol: "http",
    algorithm: "round_robin"
  })

  const fetchLoadBalancers = () => {
    const API_URL = `http://${window.location.hostname}:8080/api/v1/load-balancers`
    fetch(API_URL, {
      headers: {
        "Authorization": `Bearer ${localStorage.getItem("token")}`
      }
    })
      .then(res => {
         if (res.status === 401) {
            localStorage.removeItem("token")
            window.location.href = "/login"
         }
         return res.json()
      })
      .then(data => {
        setLoadBalancers(data || [])
      })
      .catch(console.error)
  }

  useEffect(() => {
    fetchLoadBalancers()
  }, [])

  const handleCreate = async () => {
    try {
      const payload = {
        name: formData.name,
        listen_ip: formData.listen_ip,
        listen_port: parseInt(formData.listen_port.toString(), 10),
        protocol: formData.protocol,
        algorithm: formData.algorithm,
        backend_group: {
           name: formData.name + " Group",
           backends: []
        }
      }

      const API_URL = `http://${window.location.hostname}:8080/api/v1/load-balancers`
      const res = await fetch(API_URL, {
        method: "POST",
        headers: {
          "Authorization": `Bearer ${localStorage.getItem("token")}`,
          "Content-Type": "application/json"
        },
        body: JSON.stringify(payload)
      })

      if (!res.ok) throw new Error("Failed to create load balancer")
      
      toast.success("Load Balancer created!")
      setIsCreateOpen(false)
      fetchLoadBalancers()
    } catch (err) {
      toast.error("Error creating load balancer")
    }
  }

  const handleDelete = async (id: number) => {
    if (!confirm("Are you sure you want to delete this Load Balancer?")) return

    try {
      const API_URL = `http://${window.location.hostname}:8080/api/v1/load-balancers/delete?id=${id}`
      const res = await fetch(API_URL, {
        method: "POST",
        headers: {
          "Authorization": `Bearer ${localStorage.getItem("token")}`
        }
      })

      if (!res.ok) throw new Error("Failed to delete")
      
      toast.success("Load Balancer deleted!")
      fetchLoadBalancers()
    } catch (err) {
      toast.error("Error deleting load balancer")
    }
  }

  return (
    <div className="grid gap-4 md:gap-8">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold tracking-tight">Load Balancers</h2>
        
        <Dialog open={isCreateOpen} onOpenChange={setIsCreateOpen}>
          <DialogTrigger asChild>
            <Button>Create Load Balancer</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Create New Load Balancer</DialogTitle>
            </DialogHeader>
            <div className="grid gap-4 py-4">
              <div className="grid gap-2">
                <label className="text-sm font-medium">Name</label>
                <Input value={formData.name} onChange={e => setFormData({...formData, name: e.target.value})} placeholder="e.g. Frontend App" />
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div className="grid gap-2">
                  <label className="text-sm font-medium">Listen IP</label>
                  <Input value={formData.listen_ip} onChange={e => setFormData({...formData, listen_ip: e.target.value})} placeholder="0.0.0.0" />
                </div>
                <div className="grid gap-2">
                  <label className="text-sm font-medium">Port</label>
                  <Input type="number" value={formData.listen_port} onChange={e => setFormData({...formData, listen_port: parseInt(e.target.value) || 80})} />
                </div>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div className="grid gap-2">
                  <label className="text-sm font-medium">Protocol</label>
                  <select 
                    className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background"
                    value={formData.protocol}
                    onChange={e => setFormData({...formData, protocol: e.target.value})}
                  >
                    <option value="http">HTTP</option>
                    <option value="https">HTTPS</option>
                    <option value="tcp">TCP</option>
                  </select>
                </div>
                <div className="grid gap-2">
                  <label className="text-sm font-medium">Algorithm</label>
                  <select 
                    className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background"
                    value={formData.algorithm}
                    onChange={e => setFormData({...formData, algorithm: e.target.value})}
                  >
                    <option value="round_robin">Round Robin</option>
                    <option value="least_conn">Least Connections</option>
                  </select>
                </div>
              </div>
              <Button onClick={handleCreate} className="mt-4">Create</Button>
            </div>
          </DialogContent>
        </Dialog>
      </div>
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Address</TableHead>
                <TableHead>Protocol</TableHead>
                <TableHead>Algorithm</TableHead>
                <TableHead>SSL</TableHead>
                <TableHead>Proxy Protocol</TableHead>
                <TableHead>Metrics</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {loadBalancers.map((lb) => (
                <TableRow key={lb.id}>
                  <TableCell className="font-medium">{lb.name}</TableCell>
                  <TableCell>{lb.listen_ip}:{lb.listen_port}</TableCell>
                  <TableCell>{lb.protocol.toUpperCase()}</TableCell>
                  <TableCell>{lb.algorithm}</TableCell>
                  <TableCell>
                    <Badge variant={lb.ssl_enabled ? "default" : "secondary"}>
                      {lb.ssl_enabled ? "Yes" : "No"}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    {lb.proxy_protocol_enabled ? (
                      <Badge variant="outline">PROXYv{lb.proxy_protocol_version}</Badge>
                    ) : (
                      <span className="text-muted-foreground">-</span>
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
                    <Button variant="outline" size="sm">Manage</Button>
                    <Button variant="destructive" size="sm" onClick={() => handleDelete(lb.id)}>Delete</Button>
                  </TableCell>
                </TableRow>
              ))}
              {loadBalancers.length === 0 && (
                <TableRow>
                  <TableCell colSpan={8} className="text-center py-8 text-muted-foreground">
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
