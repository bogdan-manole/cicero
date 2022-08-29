{
  "cicero/ci" = {
    task = "build";
    io = ''
      #lib.io.github_push
      #repo: "input-output-hk/cicero"
      #default_branch: false
    '';
  };
}
