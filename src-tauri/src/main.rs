fn main() {
    if let Err(error) = anvil::desktop::run() {
        eprintln!("Anvil: {error}");
        std::process::exit(1);
    }
}
