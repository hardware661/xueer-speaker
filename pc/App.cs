using System;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Net;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using System.Web.Script.Serialization;
using System.Windows.Forms;
using Microsoft.Win32;

[assembly: AssemblyTitle("雪儿 speaker")]
[assembly: AssemblyProduct("雪儿 speaker")]
[assembly: AssemblyVersion("1.0.0.0")]
[assembly: AssemblyFileVersion("1.0.0.0")]
[assembly: ComVisible(false)]

// XueerSpeaker.exe: the tray app on the PC. It owns the icon in the
// notification area, the menu and the panel window (WebView2), and runs the
// Go engine (xueer-speaker-pc.exe -engine) that keeps the PC connected to
// the speaker server and plays every line. The server address and the PC
// key live next to it in server.txt and pc.key; the first run asks for them.
namespace Xueer.Speaker
{
    internal static class Program
    {
        internal static string Base = "";
        internal static EventWaitHandle ShowSignal;
        internal static bool OpenAtStart;
        internal static readonly string Dir = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "xueer-speaker");

        [STAThread]
        private static int Main(string[] args)
        {
            Application.EnableVisualStyles();
            Application.SetCompatibleTextRenderingDefault(false);
            AppDomain.CurrentDomain.AssemblyResolve += Resources.Resolve;
            // --pair: just the pairing card (to look at it, or re-pair before
            // the tray starts); nothing else runs.
            if (Array.IndexOf(args, "--pair") >= 0) { using (PairForm f = new PairForm("")) f.ShowDialog(); return 0; }
            bool created;
            int rc;
            using (Mutex single = new Mutex(true, @"Local\xueer-speaker-tray", out created))
            using (EventWaitHandle show = new EventWaitHandle(false, EventResetMode.AutoReset, @"Local\xueer-speaker-show"))
            {
                // Started again (desktop shortcut) while running: open the
                // panel of the running one instead of a second tray.
                if (!created) { show.Set(); return 0; }
                ShowSignal = show;
                OpenAtStart = Array.IndexOf(args, "--tray") < 0;
                if (!Pairing.Ensure()) return 0;
                try { rc = Run(); }
                catch (Exception ex)
                {
                    MessageBox.Show("雪儿 speaker 没能打开，重新启动一次试试。\n\n错误类型：" + ex.GetType().Name, "雪儿 speaker");
                    return 1;
                }
            }
            // Started again only once the single-instance mutex is released.
            if (Restart) Process.Start(Application.ExecutablePath, "--tray");
            return rc;
        }

        internal static bool Restart;

        private static int Run()
        {
            Microsoft.Web.WebView2.Core.CoreWebView2Environment.SetLoaderDllFolderPath(Resources.LoaderDirectory());
            using (TrayApp app = new TrayApp()) Application.Run(app);
            return 0;
        }
    }

    // TrayApp is the invisible owner of everything: the icon, the menu, the
    // engine and the panel window.
    internal sealed class TrayApp : ApplicationContext
    {
        private readonly NotifyIcon icon;
        private readonly TrayMenu menu;
        private readonly Engine engine;
        private readonly Server server;
        private readonly System.Windows.Forms.Timer poll;
        private readonly Control ui = new Control();
        private PanelForm panel;
        private DateTime menuClosedAt = DateTime.MinValue, warmedAt = DateTime.MinValue;
        private bool connected, paused;

        internal TrayApp()
        {
            ui.CreateControl(); // a handle for marshalling engine events onto the UI thread
            server = new Server(Program.Base, ReadKey());
            menu = new TrayMenu(Command);
            icon = new NotifyIcon { Text = "雪儿 speaker", ContextMenuStrip = menu, Visible = true };
            menu.Opening += delegate { Refresh(); };
            icon.MouseMove += delegate { if ((DateTime.UtcNow - warmedAt).TotalSeconds > 20) { warmedAt = DateTime.UtcNow; server.Warm(); } };
            menu.Closed += delegate { menuClosedAt = DateTime.UtcNow; };
            icon.MouseUp += delegate(object sender, MouseEventArgs e)
            {
                if (e.Button == MouseButtons.Left && !menu.Visible && (DateTime.UtcNow - menuClosedAt).TotalMilliseconds > 350) OpenPanel();
            };
            menu.UpdateAutoStart(AutoStart.Enabled);
            engine = new Engine(Path.Combine(Program.Dir, "xueer-speaker-pc.exe"));
            engine.StateChanged += delegate(bool c, bool p) { ui.BeginInvoke((Action)delegate { connected = c; paused = p; ShowState(); }); };
            engine.Start();
            ShowState();
            // A second start (the desktop shortcut) asks this one to show
            // the panel; started by hand, open it now. Login autostart
            // passes --tray and stays quiet.
            Thread waiter = new Thread(() => { while (Program.ShowSignal.WaitOne()) ui.BeginInvoke((Action)OpenPanel); }) { IsBackground = true };
            waiter.Start();
            if (Program.OpenAtStart) ui.BeginInvoke((Action)OpenPanel);
            poll = new System.Windows.Forms.Timer { Interval = 3000 };
            poll.Tick += delegate { if (menu.Visible) Refresh(); };
            poll.Start();
        }

        private static string ReadKey()
        {
            try { return File.ReadAllText(Path.Combine(Program.Dir, "pc.key")).Trim(); }
            catch (IOException) { return ""; }
        }

        private void ShowState()
        {
            bool on = connected && !paused;
            Icon previous = icon.Icon;
            icon.Icon = Glyphs.TrayIcon(on);
            if (previous != null) previous.Dispose();
            icon.Text = paused ? "雪儿 speaker · 已暂停，播报全走音箱" : connected ? "雪儿 speaker · 电脑在接播报" : "雪儿 speaker · 正在连服务器";
            menu.UpdateLink(connected, paused);
        }

        // Refresh reads what plays and the volume for the open menu.
        private void Refresh()
        {
            Task.Run(() => server.Status()).ContinueWith(t =>
            {
                if (t.Status != TaskStatus.RanToCompletion || t.Result == null) return;
                ui.BeginInvoke((Action)delegate { menu.UpdateStatus(t.Result); });
            });
        }

        private void Command(string command, int value)
        {
            switch (command)
            {
                case "open": OpenPanel(); break;
                case "pause-link":
                    paused = !paused;
                    engine.Send(paused ? "pause" : "resume");
                    ShowState();
                    break;
                case "autostart":
                    AutoStart.Set(!AutoStart.Enabled);
                    menu.UpdateAutoStart(AutoStart.Enabled);
                    break;
                case "pause": case "resume": case "next": case "stop": case "play":
                    // Shown at once by the menu; then read back until it matches.
                    Task.Run(() => server.Music(command)).ContinueWith(t => { Refresh(); FollowUp(); });
                    break;
                case "volume":
                    Task.Run(() => server.Volume(value));
                    break;
                case "pair":
                    if (Pairing.Ask())
                    {
                        // A new server or key: restart so the engine, the menu
                        // and the panel all use it.
                        Program.Restart = true;
                        ExitThread();
                    }
                    break;
                case "quit": ExitThread(); break;
            }
        }

        private void FollowUp()
        {
            foreach (int ms in new[] { 400, 1200, 2500 })
                Task.Delay(ms).ContinueWith(t => Refresh());
        }

        private void OpenPanel()
        {
            if (panel == null || panel.IsDisposed) panel = new PanelForm(Program.Base + "/");
            panel.Reveal();
        }

        protected override void ExitThreadCore()
        {
            icon.Visible = false;
            base.ExitThreadCore();
        }

        protected override void Dispose(bool disposing)
        {
            if (disposing)
            {
                poll.Dispose();
                engine.Dispose();
                icon.Dispose();
                menu.Dispose();
                if (panel != null) panel.Dispose();
                ui.Dispose();
            }
            base.Dispose(disposing);
        }
    }

    // Engine runs xueer-speaker-pc.exe -engine and restarts it if it dies.
    internal sealed class Engine : IDisposable
    {
        private readonly string path;
        private Process process;
        private volatile bool stopping;
        private bool paused;
        internal event Action<bool, bool> StateChanged;

        internal Engine(string path) { this.path = path; }

        internal void Start()
        {
            if (stopping) return;
            ProcessStartInfo info = new ProcessStartInfo(path, "-engine")
            {
                UseShellExecute = false, CreateNoWindow = true,
                RedirectStandardInput = true, RedirectStandardOutput = true,
                StandardOutputEncoding = Encoding.UTF8,
            };
            try { process = Process.Start(info); }
            catch (Exception)
            {
                if (StateChanged != null) StateChanged(false, paused);
                RetryLater();
                return;
            }
            if (paused) Send("pause");
            Process mine = process;
            Task.Run(() =>
            {
                JavaScriptSerializer json = new JavaScriptSerializer();
                string line;
                while ((line = mine.StandardOutput.ReadLine()) != null)
                {
                    try
                    {
                        var state = json.Deserialize<System.Collections.Generic.Dictionary<string, object>>(line);
                        if (StateChanged != null) StateChanged((bool)state["connected"], (bool)state["paused"]);
                    }
                    catch (Exception) { }
                }
                if (StateChanged != null) StateChanged(false, paused);
                RetryLater();
            });
        }

        private void RetryLater()
        {
            if (stopping) return;
            Task.Delay(5000).ContinueWith(t => Start());
        }

        internal void Send(string line)
        {
            paused = line == "pause" ? true : line == "resume" ? false : paused;
            try { if (process != null && !process.HasExited) { process.StandardInput.WriteLine(line); process.StandardInput.Flush(); } }
            catch (Exception) { }
        }

        public void Dispose()
        {
            stopping = true;
            if (process == null) return;
            try { process.StandardInput.Close(); } catch (Exception) { }
            // The engine finishes the sentence it is saying before it exits.
            try { if (!process.WaitForExit(35000)) process.Kill(); } catch (Exception) { }
            process.Dispose();
        }
    }

    // Status is what the speaker server says (GET /pc/status).
    internal sealed class Status
    {
        internal string Playing = "", Artist = "", Where = "";
        internal bool Paused, PC;
        internal int Volume = -1;
    }

    // Server talks to the speaker server with the PC key, through the
    // Windows proxy like the engine.
    internal sealed class Server
    {
        private readonly string origin, key;
        // One proxy lookup, kept-alive connections and no Expect header keep a
        // click under a second; Warm() opens the connection when the menu opens.
        private IWebProxy proxy;
        private DateTime proxyAt;

        internal Server(string origin, string key)
        {
            this.origin = origin; this.key = key;
            ServicePointManager.SecurityProtocol |= SecurityProtocolType.Tls12;
            ServicePointManager.Expect100Continue = false;
            ServicePointManager.DefaultConnectionLimit = 6;
            ServicePoint sp = ServicePointManager.FindServicePoint(new Uri(origin));
            sp.MaxIdleTime = 10 * 60 * 1000;
            sp.SetTcpKeepAlive(true, 30000, 10000);
        }

        private IWebProxy Proxy()
        {
            if (proxy == null || DateTime.UtcNow - proxyAt > TimeSpan.FromMinutes(5))
            {
                proxy = WebRequest.GetSystemWebProxy();
                proxyAt = DateTime.UtcNow;
            }
            return proxy;
        }

        private HttpWebRequest Request(string method, string path)
        {
            HttpWebRequest r = (HttpWebRequest)WebRequest.Create(origin + path);
            r.Method = method;
            r.Timeout = 8000;
            r.KeepAlive = true;
            r.ServicePoint.Expect100Continue = false;
            r.Headers["Authorization"] = "Bearer " + key;
            r.Proxy = Proxy();
            return r;
        }

        // Warm opens (or keeps) the connection so the next click goes at once.
        internal void Warm() { Task.Run(() => Status()); }

        internal Status Status()
        {
            try
            {
                HttpWebRequest r = Request("GET", "/pc/status");
                using (WebResponse resp = r.GetResponse())
                using (StreamReader reader = new StreamReader(resp.GetResponseStream(), Encoding.UTF8))
                {
                    var d = new JavaScriptSerializer().Deserialize<System.Collections.Generic.Dictionary<string, object>>(reader.ReadToEnd());
                    Status s = new Status();
                    object v;
                    if (d.TryGetValue("playing", out v)) s.Playing = v as string ?? "";
                    if (d.TryGetValue("artist", out v)) s.Artist = v as string ?? "";
                    if (d.TryGetValue("where", out v)) s.Where = v as string ?? "";
                    if (d.TryGetValue("paused", out v)) s.Paused = v is bool && (bool)v;
                    if (d.TryGetValue("pc", out v)) s.PC = v is bool && (bool)v;
                    if (d.TryGetValue("volume", out v) && v is int) s.Volume = (int)v;
                    return s;
                }
            }
            catch (Exception) { return null; }
        }

        internal void Music(string action)
        {
            if (action == "play") { Post("/pc/play", "{\"what\":\"liked\"}"); return; }
            Post("/pc/music/" + action, null);
        }

        internal void Volume(int v) { Post("/pc/volume", "{\"volume\":" + v + "}"); }

        private void Post(string path, string body)
        {
            try
            {
                HttpWebRequest r = Request("POST", path);
                byte[] data = Encoding.UTF8.GetBytes(body ?? "");
                r.ContentType = "application/json";
                r.ContentLength = data.Length;
                using (Stream s = r.GetRequestStream()) s.Write(data, 0, data.Length);
                r.GetResponse().Close();
            }
            catch (Exception) { }
        }
    }

    // Pairing holds the server address and the PC key (server.txt, pc.key)
    // and asks for them on the first run; the panel's 接入 page shows both.
    internal static class Pairing
    {
        private static string ServerFile { get { return Path.Combine(Program.Dir, "server.txt"); } }
        private static string KeyFile { get { return Path.Combine(Program.Dir, "pc.key"); } }

        internal static bool Ensure()
        {
            try { Program.Base = File.ReadAllText(ServerFile).Trim().TrimEnd('/'); } catch (IOException) { }
            string key = "";
            try { key = File.ReadAllText(KeyFile).Trim(); } catch (IOException) { }
            if (Program.Base.StartsWith("http") && key.Length > 0) return true;
            return Ask();
        }

        // Ask shows the pairing card; true = saved.
        internal static bool Ask()
        {
            using (PairForm f = new PairForm(Program.Base))
            {
                if (f.ShowDialog() != DialogResult.OK) return false;
                Directory.CreateDirectory(Program.Dir);
                File.WriteAllText(ServerFile, f.Url);
                File.WriteAllText(KeyFile, f.Key);
                Program.Base = f.Url;
                return true;
            }
        }
    }

    internal static class AutoStart
    {
        private const string RunKey = @"Software\Microsoft\Windows\CurrentVersion\Run", Name = "XueerSpeakerPC";
        internal static bool Enabled
        {
            get { using (RegistryKey k = Registry.CurrentUser.OpenSubKey(RunKey)) return k != null && k.GetValue(Name) != null; }
        }
        internal static void Set(bool on)
        {
            using (RegistryKey k = Registry.CurrentUser.OpenSubKey(RunKey, true))
            {
                if (on) k.SetValue(Name, "\"" + Application.ExecutablePath + "\" --tray");
                else k.DeleteValue(Name, false);
            }
        }
    }

    internal static class Resources
    {
        internal const string SdkVersion = "1.0.4258.31";
        internal static byte[] Bytes(string name)
        {
            using (Stream stream = Assembly.GetExecutingAssembly().GetManifestResourceStream(name))
            using (MemoryStream buffer = new MemoryStream())
            {
                if (stream == null) throw new InvalidDataException("Missing embedded resource " + name);
                stream.CopyTo(buffer); return buffer.ToArray();
            }
        }

        internal static Assembly Resolve(object sender, ResolveEventArgs args)
        {
            string name = new AssemblyName(args.Name).Name;
            if (name != "Microsoft.Web.WebView2.Core" && name != "Microsoft.Web.WebView2.WinForms") return null;
            return Assembly.Load(Bytes("Xueer.SDK." + name + ".dll"));
        }

        internal static string LoaderDirectory()
        {
            string directory = Path.Combine(Program.Dir, "Runtime", SdkVersion, "x64");
            Directory.CreateDirectory(directory);
            string file = Path.Combine(directory, "WebView2Loader.dll");
            byte[] expected = Bytes("Xueer.SDK.WebView2Loader.dll");
            if (!File.Exists(file) || File.ReadAllBytes(file).Length != expected.Length) File.WriteAllBytes(file, expected);
            return directory;
        }
    }
}
