node { parallel O1: { stage('X') { catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') { error 'caught' } }
                      stage('Z') { sleep 1; error 'real' } },
 O2: { stage('Q') { echo 'ok' } } }
