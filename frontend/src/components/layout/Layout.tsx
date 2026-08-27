import { Link } from "react-router-dom"
import { Activity, LayoutDashboard, Server } from "lucide-react"

export function Layout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen w-full flex-col bg-muted/40">
      <aside className="fixed inset-y-0 left-0 z-10 hidden w-64 flex-col border-r bg-background sm:flex">
        <div className="flex h-14 items-center border-b px-4 lg:h-[60px] lg:px-6">
          <Link to="/" className="flex items-center gap-2 font-semibold">
            <Activity className="h-6 w-6" />
            <span className="">Balacer</span>
          </Link>
        </div>
        <nav className="flex-1 overflow-auto py-2">
          <div className="grid items-start px-2 text-sm font-medium lg:px-4">
            <Link
              to="/"
              className="flex items-center gap-3 rounded-lg px-3 py-2 text-muted-foreground transition-all hover:text-primary"
            >
              <LayoutDashboard className="h-4 w-4" />
              Dashboard
            </Link>
            <Link
              to="/lbs"
              className="flex items-center gap-3 rounded-lg bg-muted px-3 py-2 text-primary transition-all hover:text-primary"
            >
              <Server className="h-4 w-4" />
              Load Balancers
            </Link>
          </div>
        </nav>
      </aside>
      <div className="flex flex-col sm:gap-4 sm:py-4 sm:pl-64">
        <header className="sticky top-0 z-30 flex h-14 items-center justify-between gap-4 border-b bg-background px-4 sm:static sm:h-auto sm:border-0 sm:bg-transparent sm:px-6">
           <h1 className="text-xl font-semibold">Overview</h1>
           <div className="flex items-center gap-4">
             <span className="text-sm text-muted-foreground hidden md:inline">Logged in as admin</span>
             <button 
               onClick={() => {
                 localStorage.removeItem("token");
                 window.location.href = "/login";
               }}
               className="text-sm font-medium text-red-500 hover:text-red-600 transition-colors"
             >
               Logout
             </button>
           </div>
        </header>
        <main className="grid flex-1 items-start gap-4 p-4 sm:px-6 sm:py-0 md:gap-8">
          {children}
        </main>
      </div>
    </div>
  )
}
