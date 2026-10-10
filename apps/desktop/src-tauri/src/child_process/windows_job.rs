//! Windows process-tree containment for supervised children.
//!
//! Each child runs in its OWN Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`:
//!
//! - **One job per child, not one app-wide job.** Stops are per child (a
//!   bridge restart must not touch the sidecars; uninstalling one sidecar must
//!   not touch the others), and `TerminateJobObject` kills everything in a
//!   job. An app-wide job would also have to contain the app itself to catch
//!   every child atomically, and then every process the app launches — the
//!   updater's installer, a browser opened for a link — would die when the app
//!   exits.
//! - **Kill-on-close covers crashes.** The job handle lives in the [`Job`]
//!   owned by the child's [`super::Child`]; when the app exits for any reason
//!   (including a crash or a kill from Task Manager) the OS closes the handle
//!   and terminates the child together with everything it spawned. Dropping a
//!   `Child` therefore ends its tree — callers only drop one after it was
//!   stopped or has exited, which also reaps any grandchild it left behind.
//! - **No escape window.** The child is created suspended, assigned to the job,
//!   and only then resumed, so nothing it spawns can be created outside the
//!   job. Jobs nest (Windows 8+), so this works when the app itself was
//!   launched inside a job.
use std::io;
use std::os::windows::io::{AsRawHandle, HandleOrInvalid, HandleOrNull, OwnedHandle};
use std::os::windows::process::CommandExt;
use std::process::{Child as StdChild, Command};

use windows_sys::Win32::Foundation::HANDLE;
use windows_sys::Win32::System::Diagnostics::ToolHelp::{
    CreateToolhelp32Snapshot, Thread32First, Thread32Next, TH32CS_SNAPTHREAD, THREADENTRY32,
};
use windows_sys::Win32::System::JobObjects::{
    AssignProcessToJobObject, CreateJobObjectW, JobObjectExtendedLimitInformation,
    SetInformationJobObject, TerminateJobObject, JOBOBJECT_EXTENDED_LIMIT_INFORMATION,
    JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
};
use windows_sys::Win32::System::Threading::{
    OpenThread, ResumeThread, CREATE_NO_WINDOW, CREATE_SUSPENDED, THREAD_SUSPEND_RESUME,
};

/// Exit code reported for processes ended by [`Job::terminate`].
const TERMINATED_EXIT_CODE: u32 = 1;

/// A kill-on-close Job Object holding one supervised child and its
/// descendants.
pub(super) struct Job(OwnedHandle);

impl Job {
    fn new() -> io::Result<Self> {
        // SAFETY: null attributes/name create an anonymous job with default
        // security; the returned handle (or null) is owned by us.
        let raw = unsafe { CreateJobObjectW(std::ptr::null(), std::ptr::null()) };
        // SAFETY: `raw` comes straight from CreateJobObjectW (null = failure).
        let handle = OwnedHandle::try_from(unsafe { HandleOrNull::from_raw_handle(raw) })
            .map_err(|_| io::Error::last_os_error())?;
        let job = Job(handle);
        let mut info = JOBOBJECT_EXTENDED_LIMIT_INFORMATION::default();
        info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
        // SAFETY: `info` is a valid JOBOBJECT_EXTENDED_LIMIT_INFORMATION and
        // the length passed is exactly its size.
        let ok = unsafe {
            SetInformationJobObject(
                job.raw(),
                JobObjectExtendedLimitInformation,
                std::ptr::from_ref(&info).cast(),
                std::mem::size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(job)
    }

    fn raw(&self) -> HANDLE {
        self.0.as_raw_handle()
    }

    /// Kill every process in the job: the child and everything it spawned.
    pub(super) fn terminate(&self) {
        // SAFETY: a valid job handle owned by `self`.
        unsafe {
            TerminateJobObject(self.raw(), TERMINATED_EXIT_CODE);
        }
    }

    /// Number of live processes in the job (tests only).
    #[cfg(test)]
    pub(super) fn active_processes(&self) -> io::Result<u32> {
        use windows_sys::Win32::System::JobObjects::{
            JobObjectBasicAccountingInformation, QueryInformationJobObject,
            JOBOBJECT_BASIC_ACCOUNTING_INFORMATION,
        };
        let mut info = JOBOBJECT_BASIC_ACCOUNTING_INFORMATION::default();
        // SAFETY: `info` is a writable struct of exactly the size passed.
        let ok = unsafe {
            QueryInformationJobObject(
                self.raw(),
                JobObjectBasicAccountingInformation,
                std::ptr::from_mut(&mut info).cast(),
                std::mem::size_of::<JOBOBJECT_BASIC_ACCOUNTING_INFORMATION>() as u32,
                std::ptr::null_mut(),
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(info.ActiveProcesses)
    }
}

/// Spawn `cmd` (no console window) inside a fresh kill-on-close job: created
/// suspended, assigned, then resumed. On any failure the half-started child is
/// killed and the error returned — a child is never left running uncontained.
pub(super) fn spawn_in_job(cmd: &mut Command) -> io::Result<(StdChild, Job)> {
    cmd.creation_flags(CREATE_NO_WINDOW | CREATE_SUSPENDED);
    let job = Job::new()?;
    let mut child = cmd.spawn()?;
    let contained = assign(&job, &child).and_then(|()| resume_main_thread(child.id()));
    if let Err(e) = contained {
        let _ = child.kill();
        let _ = child.wait();
        return Err(e);
    }
    Ok((child, job))
}

fn assign(job: &Job, child: &StdChild) -> io::Result<()> {
    // SAFETY: both handles are valid and owned for the duration of the call.
    let ok = unsafe { AssignProcessToJobObject(job.raw(), child.as_raw_handle()) };
    if ok == 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

/// Resume the (only) thread of a process created with `CREATE_SUSPENDED`.
/// `std::process::Child` does not expose the primary thread handle, so the
/// thread is found through a ToolHelp snapshot of the system's threads.
fn resume_main_thread(pid: u32) -> io::Result<()> {
    // SAFETY: plain snapshot request; INVALID_HANDLE_VALUE signals failure.
    let raw = unsafe { CreateToolhelp32Snapshot(TH32CS_SNAPTHREAD, 0) };
    // SAFETY: `raw` comes straight from CreateToolhelp32Snapshot.
    let snapshot = OwnedHandle::try_from(unsafe { HandleOrInvalid::from_raw_handle(raw) })
        .map_err(|_| io::Error::last_os_error())?;
    let mut entry = THREADENTRY32 {
        dwSize: std::mem::size_of::<THREADENTRY32>() as u32,
        ..Default::default()
    };
    let mut resumed = 0;
    // SAFETY: a valid snapshot handle and an initialized entry (dwSize set).
    let mut more = unsafe { Thread32First(snapshot.as_raw_handle(), &mut entry) } != 0;
    while more {
        if entry.th32OwnerProcessID == pid {
            // SAFETY: plain open by thread id; null signals failure.
            let raw = unsafe { OpenThread(THREAD_SUSPEND_RESUME, 0, entry.th32ThreadID) };
            // SAFETY: `raw` comes straight from OpenThread.
            let thread = OwnedHandle::try_from(unsafe { HandleOrNull::from_raw_handle(raw) })
                .map_err(|_| io::Error::last_os_error())?;
            // SAFETY: a valid thread handle opened with THREAD_SUSPEND_RESUME.
            if unsafe { ResumeThread(thread.as_raw_handle()) } == u32::MAX {
                return Err(io::Error::last_os_error());
            }
            resumed += 1;
        }
        // SAFETY: same snapshot and entry as above.
        more = unsafe { Thread32Next(snapshot.as_raw_handle(), &mut entry) } != 0;
    }
    if resumed == 0 {
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            format!("suspended child {pid} has no thread to resume"),
        ));
    }
    Ok(())
}
